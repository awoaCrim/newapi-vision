package vision

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math/bits"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/corona10/goimagehash"
	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
	"github.com/samber/lo"
	"golang.org/x/sync/singleflight"
)

// 图片描述缓存（LRU，TTL 10 分钟，最多 1000 条）
var imageDescCache = hot.NewHotCache[string, string](hot.LRU, 1000).
	WithTTL(10 * time.Minute).Build()

// requestCache 请求级去重：同一请求中相同图片只调一次视觉模型
var requestCache = hot.NewHotCache[string, map[string]string](hot.LRU, 5000).
	WithTTL(5 * time.Minute).Build()

// sfGroup 全局 singleflight：同一图片并发请求合并为一次 API 调用，防止缓存击穿
var sfGroup singleflight.Group

// L4: 跨请求 pHash 模糊缓存 —— 相似图片复用描述
type phashEntry struct {
	hash uint64
	desc string
}

type phashRingCache struct {
	mu      sync.RWMutex
	entries []phashEntry
	count   int // 当前条目数
	max     int
}

func newPhashRingCache(max int) *phashRingCache {
	return &phashRingCache{
		entries: make([]phashEntry, max),
		max:     max,
	}
}

func (c *phashRingCache) lookup(hash uint64, threshold int) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := c.count
	if n > c.max {
		n = c.max
	}
	for i := 0; i < n; i++ {
		if bits.OnesCount64(c.entries[i].hash^hash) <= threshold {
			return c.entries[i].desc, true
		}
	}
	return "", false
}

func (c *phashRingCache) store(hash uint64, desc string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.count % c.max
	c.entries[idx] = phashEntry{hash: hash, desc: desc}
	c.count++ // 自然递增无界溢出，idx = count % max 自动绕回，构成真正的环形缓冲
}

var phashCache = newPhashRingCache(500)

// ImageBlock 表示从消息中提取的图片块
type ImageBlock struct {
	MessageIdx int    // 在 messages 数组中的索引
	ContentIdx int    // 在 content 数组中的索引（顶层）
	NestedPath string // 嵌套路径，如 "messages.0.content.2.content.0"，为空则使用 MessageIdx+ContentIdx 构造
	ImageURL   string // HTTP URL 或 data: URI
	MediaType  string // 图片 MIME 类型，如 "image/png"
}

const maxBase64ImageBytes = 20 * 1024 * 1024 // 20MB

// DecodeBase64Image 解析 data: URI 并解码 base64 为 image.Image
func DecodeBase64Image(dataURI string) (image.Image, error) {
	if !strings.HasPrefix(dataURI, "data:") {
		return nil, fmt.Errorf("not a data URI")
	}
	commaIdx := strings.IndexByte(dataURI, ',')
	if commaIdx < 0 {
		return nil, fmt.Errorf("invalid data URI: missing comma")
	}
	b64Data := dataURI[commaIdx+1:]
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64Data))
	img, _, err := image.Decode(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image from base64: %w", err)
	}
	return img, nil
}

// ComputePhash 计算图片的感知哈希 (uint64)
func ComputePhash(img image.Image) (uint64, error) {
	hash, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return 0, fmt.Errorf("failed to compute perceptual hash: %w", err)
	}
	return hash.GetHash(), nil
}

// HammingDistance 计算两个 uint64 的汉明距离
func HammingDistance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// LookupCachedDescription 仅查询 L4/L2 缓存，不发起任何 API 调用
// 用于历史消息中的旧图——有缓存则复用描述，无缓存则保留原图让模型直接查看
func LookupCachedDescription(imageURL string, config dto.VisionUserSetting, phash *uint64) (string, bool) {
	// L4: 跨请求 pHash 模糊缓存
	if phash != nil && config.PhashThreshold > 0 {
		if desc, found := phashCache.lookup(*phash, config.PhashThreshold); found {
			return desc, true
		}
	}
	// L2: 全局 LRU 缓存（exact URL match）
	cacheKey := hashImageURL(imageURL, config.PromptTemplate)
	if desc, found, _ := imageDescCache.Get(cacheKey); found {
		return desc, true
	}
	return "", false
}

// visionCallResult 封装 vision API 调用的返回数据
type visionCallResult struct {
	description      string
	promptTokens     int
	completionTokens int
	totalTokens      int
}

// AnalyzeImage 调用视觉模型获取图片的文字描述
// c 用于通道查找和计费
// requestID 用于请求级去重和防模型缓存
// phash 为预计算的感知哈希，用于 L4 模糊缓存（nil 表示跳过 L4）
// 返回描述文本和是否命中缓存，如果失败返回错误（必须严格失败，不允许返回空描述）
func AnalyzeImage(c *gin.Context, ctx context.Context, config dto.VisionUserSetting, imageURL string, requestID string, phash *uint64) (desc string, cached bool, err error) {
	if imageURL == "" {
		return "", false, fmt.Errorf("empty image URL")
	}

	// L4: 跨请求 pHash 模糊缓存（在 L1 之前检查，缓存范围更广）
	if phash != nil && config.PhashThreshold > 0 {
		if desc, found := phashCache.lookup(*phash, config.PhashThreshold); found {
			storeInRequestCache(requestID, imageURL, desc)
			return desc, true, nil
		}
	}

	// L1：请求级去重 —— 同一 requestID 中同一 URL 只调一次
	if requestID != "" {
		if reqMap, found, _ := requestCache.Get(requestID); found && reqMap != nil {
			if desc, exists := reqMap[imageURL]; exists {
				return desc, true, nil
			}
		}
	}

	// L2：全局 LRU 缓存 —— 跨请求复用（key 包含 prompt hash，不同 prompt 不互串）
	cacheKey := hashImageURL(imageURL, config.PromptTemplate)
	if desc, found, _ := imageDescCache.Get(cacheKey); found {
		// 存入请求级缓存
		storeInRequestCache(requestID, imageURL, desc)
		return desc, true, nil
	}

	// 图片大小限制（data: URI 格式）
	if strings.HasPrefix(imageURL, "data:") {
		if len(imageURL) > maxBase64ImageBytes {
			return "", false, fmt.Errorf("base64 image exceeds size limit (%d bytes)", maxBase64ImageBytes)
		}
	}

	// SSRF 防护：HTTP URL 使用系统 FetchSettings 配置
	if strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://") {
		if err := validateImageURL(imageURL); err != nil {
			return "", false, err
		}
	}

	// 防模型缓存：将 requestID 注入到 prompt，使每次请求的 prompt 都不同
	promptText := config.PromptTemplate
	if requestID != "" {
		promptText = promptText + "\n[context_id:" + requestID + "]"
	}

	// L3: singleflight —— 同一图片的并发 API 调用合并为一次，防止缓存击穿
	callResult, err, shared := sfGroup.Do(cacheKey, func() (interface{}, error) {
		// 查找通道：从系统模型广场中选取支持 vision 模型的通道
		tokenGroup := common.GetContextKeyString(c, constant.ContextKeyTokenGroup)
		retryParam := &service.RetryParam{
			Ctx:         c,
			TokenGroup:  tokenGroup,
			ModelName:   config.VisionModelName,
			RequestPath: "/v1/chat/completions",
		}
		channel, _, chErr := service.CacheGetRandomSatisfiedChannel(retryParam)
		if chErr != nil {
			return nil, fmt.Errorf("failed to find channel for vision model '%s': %w", config.VisionModelName, chErr)
		}
		if channel == nil {
			return nil, fmt.Errorf("no available channel for vision model '%s' — please ensure the model is configured in the model marketplace", config.VisionModelName)
		}

		// 通过 relay adaptor 系统调用，兼容所有渠道类型

		// 1. 构造虚拟 gin.Context（参照 channel-test.go:testChannel）
		w := httptest.NewRecorder()
		visionCtx, _ := gin.CreateTestContext(w)
		visionCtx.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", nil)
		visionCtx.Request.Header.Set("Content-Type", "application/json")

		// 2. 注入用户上下文
		userId := c.GetInt("id")
		userCache, _ := model.GetUserCache(userId)
		if userCache != nil {
			userCache.WriteContext(visionCtx)
		}
		visionCtx.Set("id", userId)

		// 3. 注入渠道上下文（内联 SetupContextForSelectedChannel 逻辑，避免循环依赖）
		visionCtx.Set("original_model", config.VisionModelName)
		common.SetContextKey(visionCtx, constant.ContextKeyChannelId, channel.Id)
		common.SetContextKey(visionCtx, constant.ContextKeyChannelName, channel.Name)
		common.SetContextKey(visionCtx, constant.ContextKeyChannelType, channel.Type)
		common.SetContextKey(visionCtx, constant.ContextKeyChannelCreateTime, channel.CreatedTime)
		common.SetContextKey(visionCtx, constant.ContextKeyChannelSetting, channel.GetSetting())
		common.SetContextKey(visionCtx, constant.ContextKeyChannelOtherSetting, channel.GetOtherSettings())
		common.SetContextKey(visionCtx, constant.ContextKeyChannelParamOverride, channel.GetParamOverride())
		common.SetContextKey(visionCtx, constant.ContextKeyChannelHeaderOverride, channel.GetHeaderOverride())
		if channel.OpenAIOrganization != nil && *channel.OpenAIOrganization != "" {
			common.SetContextKey(visionCtx, constant.ContextKeyChannelOrganization, *channel.OpenAIOrganization)
		}
		common.SetContextKey(visionCtx, constant.ContextKeyChannelAutoBan, channel.GetAutoBan())
		common.SetContextKey(visionCtx, constant.ContextKeyChannelModelMapping, channel.GetModelMapping())
		common.SetContextKey(visionCtx, constant.ContextKeyChannelStatusCodeMapping, channel.GetStatusCodeMapping())
		key, _, keyErr := channel.GetNextEnabledKey()
		if keyErr != nil {
			return nil, fmt.Errorf("failed to get channel key for vision: %s", keyErr.Error())
		}
		common.SetContextKey(visionCtx, constant.ContextKeyChannelKey, key)
		common.SetContextKey(visionCtx, constant.ContextKeyChannelBaseUrl, channel.GetBaseURL())
		common.SetContextKey(visionCtx, constant.ContextKeySystemPromptOverride, false)

		// 4. 构造 OpenAI chat completions 请求（使用多模态消息格式）
		request := &dto.GeneralOpenAIRequest{
			Model:  config.VisionModelName,
			Stream: lo.ToPtr(false),
			Messages: []dto.Message{
				{
					Role: "user",
				},
			},
			MaxTokens: lo.ToPtr(uint(4096)),
		}
		// 设置多模态内容（text + image_url）
		request.Messages[0].SetMediaContent([]dto.MediaContent{
			{
				Type: "text",
				Text: promptText,
			},
			{
				Type: "image_url",
				ImageUrl: &dto.MessageImageUrl{
					Url:    imageURL,
					Detail: "high",
				},
			},
		})

		// 5. 生成 RelayInfo
		info, genErr := relaycommon.GenRelayInfo(visionCtx, types.RelayFormatOpenAI, request, nil)
		if genErr != nil {
			return nil, fmt.Errorf("failed to generate relay info for vision: %w", genErr)
		}
		info.InitChannelMeta(visionCtx)

		// 6. 模型映射
		if mapErr := helper.ModelMappedHelper(visionCtx, info, request); mapErr != nil {
			return nil, fmt.Errorf("failed to map model for vision: %w", mapErr)
		}

		// 7. 获取 adaptor
		apiType, _ := common.ChannelType2APIType(channel.Type)
		adaptor := relay.GetAdaptor(apiType)
		if adaptor == nil {
			return nil, fmt.Errorf("invalid api type %d for vision channel #%d", apiType, channel.Id)
		}
		adaptor.Init(info)

		// 8. 转换请求
		convertedRequest, convErr := adaptor.ConvertOpenAIRequest(visionCtx, info, request)
		if convErr != nil {
			return nil, fmt.Errorf("failed to convert vision request: %w", convErr)
		}

		jsonData, marshalErr := common.Marshal(convertedRequest)
		if marshalErr != nil {
			return nil, fmt.Errorf("failed to marshal vision request: %w", marshalErr)
		}

		requestBody := bytes.NewBuffer(jsonData)
		visionCtx.Request.Body = io.NopCloser(bytes.NewBuffer(jsonData))

		// 9. 发送请求
		resp, doErr := adaptor.DoRequest(visionCtx, info, requestBody)
		if doErr != nil {
			return nil, fmt.Errorf("vision model request failed: %w", doErr)
		}

		var httpResp *http.Response
		if resp != nil {
			var ok bool
			httpResp, ok = resp.(*http.Response)
			if !ok {
				return nil, fmt.Errorf("vision adaptor returned unexpected response type %T", resp)
			}
			if httpResp.StatusCode != http.StatusOK {
				relayErr := service.RelayErrorHandler(ctx, httpResp, true)
				return nil, fmt.Errorf("vision model returned status %d: %v", httpResp.StatusCode, relayErr)
			}
		}

		// 10. 解析响应
		usageA, respErr := adaptor.DoResponse(visionCtx, httpResp, info)
		if respErr != nil {
			return nil, fmt.Errorf("failed to parse vision response: %s", respErr.Error())
		}

		// 从 ResponseRecorder 获取响应体，提取描述文本
		recorderResult := w.Result()
		respBody, readErr := io.ReadAll(recorderResult.Body)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read vision response body: %w", readErr)
		}

		// 解析 choices[0].message.content
		var responseStruct struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if jsonErr := common.Unmarshal(respBody, &responseStruct); jsonErr != nil {
			return nil, fmt.Errorf("failed to parse vision response JSON: %w, body: %s", jsonErr, truncateBytes(respBody, 200))
		}

		if len(responseStruct.Choices) == 0 {
			return nil, fmt.Errorf("vision model returned empty choices")
		}

		description := strings.TrimSpace(responseStruct.Choices[0].Message.Content)
		if description == "" {
			return nil, fmt.Errorf("vision model returned empty description")
		}

		// 提取 usage 用于计费
		var promptTokens, completionTokens, totalTokens int
		usage, _ := usageA.(*dto.Usage)
		if usage != nil {
			promptTokens = usage.PromptTokens
			completionTokens = usage.CompletionTokens
			totalTokens = usage.TotalTokens
		}

		// 计费：通过 PostTextConsumeQuota 使用标准计费逻辑（含模型价格、倍率、分组比例）
		if userId > 0 && totalTokens > 0 {
			info.IsStream = false
			service.PostTextConsumeQuota(visionCtx, info, usage, []string{"[vision] " + truncateString(description, 200)})
			logger.LogInfo(c, fmt.Sprintf("[vision] billed via PostTextConsumeQuota (model=%s, channel=%d, tokens=%d)",
				config.VisionModelName, channel.Id, totalTokens))
		}

		return &visionCallResult{
			description:      description,
			promptTokens:     promptTokens,
			completionTokens: completionTokens,
			totalTokens:      totalTokens,
		}, nil
	})
	if err != nil {
		return "", false, err
	}

	vcr := callResult.(*visionCallResult)
	description := vcr.description

	// 存入 L4 pHash 缓存（跨请求模糊匹配）
	if !shared && phash != nil && config.PhashThreshold > 0 {
		phashCache.store(*phash, description)
	}

	// 存入两级缓存（仅第一个调用者写入，shared=false 时执行，防止并发写）
	if !shared {
		imageDescCache.Set(cacheKey, description)
	}
	storeInRequestCache(requestID, imageURL, description)

	// singleflight 共享用户：shared=true 表示本次调用被合并，
	// 实际 API 调用由第一个请求者通过 PostTextConsumeQuota 付费，
	// 共享用户不重复扣费（同一 API 调用只应收费一次）

	return description, false, nil
}

func truncateString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

func truncateBytes(data []byte, maxLen int) string {
	s := string(data)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// hashImageURL 对图片 URL + prompt 做轻量哈希
// 只取前 256 字节避免对大 base64 做全量哈希
// prompt 参与 hash 确保不同 prompt 模板不共享缓存
func hashImageURL(imageURL string, promptTemplate string) string {
	key := imageURL
	if len(key) > 256 {
		key = key[:256]
	}
	// 混入 prompt（取前 100 字节）
	if len(promptTemplate) > 100 {
		key += promptTemplate[:100]
	} else {
		key += promptTemplate
	}
	h := md5.Sum([]byte(key))
	return hex.EncodeToString(h[:])
}

// storeInRequestCache 将描述存入请求级缓存，同一请求中相同 URL 直接复用
func storeInRequestCache(requestID string, imageURL string, desc string) {
	if requestID == "" {
		return
	}
	reqMap, found, _ := requestCache.Get(requestID)
	if !found || reqMap == nil {
		reqMap = make(map[string]string)
	}
	reqMap[imageURL] = desc
	requestCache.Set(requestID, reqMap)
}

// validateImageURL 校验图片 URL，使用系统 FetchSettings 防止 SSRF 攻击
func validateImageURL(imageURL string) error {
	parsed, err := url.Parse(imageURL)
	if err != nil {
		return fmt.Errorf("invalid image URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("image URL must use https: %s", parsed.Scheme)
	}

	fs := system_setting.GetFetchSetting()
	err = common.ValidateURLWithFetchSetting(
		imageURL,
		fs.EnableSSRFProtection,
		fs.AllowPrivateIp,
		fs.DomainFilterMode,
		fs.IpFilterMode,
		fs.DomainList,
		fs.IpList,
		fs.AllowedPorts,
		fs.ApplyIPFilterForDomain,
	)
	if err != nil {
		return fmt.Errorf("image URL rejected by SSRF protection: %w", err)
	}
	return nil
}

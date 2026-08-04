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
// entry 内 map 受互斥锁保护，防止并发写入（多图并行分析时共享同一 entry）
type requestCacheEntry struct {
	mu sync.Mutex
	m  map[string]string
}

var requestCache = hot.NewHotCache[string, *requestCacheEntry](hot.LRU, 5000).
	WithTTL(5 * time.Minute).Build()

// requestCacheInitMu 保护 requestCache entry 的创建，避免并发 miss 时创建多个 entry 互相覆盖
var requestCacheInitMu sync.Mutex

// sfGroup 全局 singleflight：同一图片并发请求合并为一次 API 调用，防止缓存击穿
var sfGroup singleflight.Group

// L4: 跨请求 pHash 模糊缓存 —— 相似图片复用描述
// entry 记录用户、视觉模型与 prompt 哈希，防止跨用户/跨模型/跨 prompt 串用
type phashEntry struct {
	userId     int
	model      string
	promptHash string
	hash       uint64
	desc       string
	ts         int64 // 写入时间戳，用于 TTL 过期
}

const phashCacheTTL = 10 * time.Minute

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

func (c *phashRingCache) lookup(userId int, model string, promptHash string, hash uint64, threshold int) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := c.count
	if n > c.max {
		n = c.max
	}
	now := time.Now().Unix()
	for i := 0; i < n; i++ {
		e := c.entries[i]
		if now-e.ts > int64(phashCacheTTL.Seconds()) {
			continue // 过期条目视为未命中
		}
		if e.userId == userId && e.model == model && e.promptHash == promptHash && bits.OnesCount64(e.hash^hash) <= threshold {
			return e.desc, true
		}
	}
	return "", false
}

func (c *phashRingCache) store(userId int, model string, promptHash string, hash uint64, desc string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.count % c.max
	c.entries[idx] = phashEntry{userId: userId, model: model, promptHash: promptHash, hash: hash, desc: desc, ts: time.Now().Unix()}
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

const MaxBase64ImageBytes = 20 * 1024 * 1024 // 20MB
const maxDecodedImageBytes = 15 * 1024 * 1024 // 15MB，base64 解码后上限（约等于 20MB 编码串）
const maxImageDimension = 8192            // 单边最大像素
const maxImagePixels = 20 * 1000 * 1000   // 总像素上限（约 20MP），防止解码时内存暴涨
const maxImageDownloadBytes = 15 * 1024 * 1024 // URL 图片下载上限（15MB）

// DecodeBase64Image 解析 data: URI 并解码 base64 为 image.Image
// 解码前先读取图片头检查尺寸与像素上限，并限制解码输出字节数，防止超大图片触发内存 DoS
func DecodeBase64Image(dataURI string) (image.Image, error) {
	if !strings.HasPrefix(dataURI, "data:") {
		return nil, fmt.Errorf("not a data URI")
	}
	commaIdx := strings.IndexByte(dataURI, ',')
	if commaIdx < 0 {
		return nil, fmt.Errorf("invalid data URI: missing comma")
	}
	b64Data := dataURI[commaIdx+1:]
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64Data))
	limited := io.LimitReader(decoder, maxDecodedImageBytes+1)

	// 先读图片头，校验尺寸与像素上限（DecodeConfig 只解析头部，不分配全图内存）
	cfg, format, err := image.DecodeConfig(limited)
	if err != nil {
		return nil, fmt.Errorf("failed to read image header from base64: %w", err)
	}
	if err := validateImageSize(cfg); err != nil {
		return nil, err
	}

	// 重新解码完整图片（decoder 是流式的，需重建 reader）
	decoder2 := base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64Data))
	img, _, err := image.Decode(io.LimitReader(decoder2, maxDecodedImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image from base64: %w", err)
	}
	_ = format
	return img, nil
}

// DownloadImageForPhash 安全下载 URL 图片用于 pHash 计算：
// SSRF 校验（validateImageURL）→ Content-Length 限制 → 流式读取大小限制 → 尺寸/像素上限
func DownloadImageForPhash(imageURL string) (image.Image, error) {
	if err := validateImageURL(imageURL); err != nil {
		return nil, err
	}

	resp, err := service.GetHttpClient().Get(imageURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image download returned status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxImageDownloadBytes {
		return nil, fmt.Errorf("image download exceeds size limit (%d bytes)", maxImageDownloadBytes)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageDownloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read image body: %w", err)
	}
	if len(data) > maxImageDownloadBytes {
		return nil, fmt.Errorf("image download exceeds size limit (%d bytes)", maxImageDownloadBytes)
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to read image header from URL: %w", err)
	}
	if err := validateImageSize(cfg); err != nil {
		return nil, err
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image from URL: %w", err)
	}
	return img, nil
}

// validateImageSize 校验图片宽高与总像素上限
func validateImageSize(cfg image.Config) error {
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("invalid image dimensions: %dx%d", cfg.Width, cfg.Height)
	}
	if cfg.Width > maxImageDimension || cfg.Height > maxImageDimension {
		return fmt.Errorf("image dimensions %dx%d exceed limit %d", cfg.Width, cfg.Height, maxImageDimension)
	}
	if cfg.Width*cfg.Height > maxImagePixels {
		return fmt.Errorf("image pixel count %d exceeds limit %d", cfg.Width*cfg.Height, maxImagePixels)
	}
	return nil
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
// 缓存键包含 userId/model/prompt，杜绝跨用户与跨模型串用
func LookupCachedDescription(userId int, imageURL string, config dto.VisionUserSetting, phash *uint64) (string, bool) {
	// L4: 跨请求 pHash 模糊缓存
	if phash != nil && config.PhashThreshold > 0 {
		promptHash := hashPrompt(config.PromptTemplate)
		if desc, found := phashCache.lookup(userId, config.VisionModelName, promptHash, *phash, config.PhashThreshold); found {
			return desc, true
		}
	}
	// L2: 全局 LRU 缓存（exact URL match）
	cacheKey := buildCacheKey(userId, imageURL, config)
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

	userId := c.GetInt("id")

	// L4: 跨请求 pHash 模糊缓存（在 L1 之前检查，缓存范围更广）
	promptHash := hashPrompt(config.PromptTemplate)
	if phash != nil && config.PhashThreshold > 0 {
		if desc, found := phashCache.lookup(userId, config.VisionModelName, promptHash, *phash, config.PhashThreshold); found {
			storeInRequestCache(requestID, imageURL, desc)
			return desc, true, nil
		}
	}

	// L1：请求级去重 —— 同一 requestID 中同一 URL 只调一次
	if requestID != "" {
		if entry, found, _ := requestCache.Get(requestID); found && entry != nil {
			entry.mu.Lock()
			desc, exists := entry.m[imageURL]
			entry.mu.Unlock()
			if exists {
				return desc, true, nil
			}
		}
	}

	// L2：全局 LRU 缓存 —— 跨请求复用（key 含 userId/model/prompt/完整图片哈希，不同用户不互串）
	cacheKey := buildCacheKey(userId, imageURL, config)
	if desc, found, _ := imageDescCache.Get(cacheKey); found {
		// 存入请求级缓存
		storeInRequestCache(requestID, imageURL, desc)
		return desc, true, nil
	}

	// 图片大小限制（data: URI 格式）
	if strings.HasPrefix(imageURL, "data:") {
		if len(imageURL) > MaxBase64ImageBytes {
			return "", false, fmt.Errorf("base64 image exceeds size limit (%d bytes)", MaxBase64ImageBytes)
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

	// L3: singleflight —— 同一用户+模型+图片的并发 API 调用合并为一次，防止缓存击穿
	// key 含用户/模型维度，不同用户的请求不会合并计费
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

		// 2.1 继承原请求的 token/group 计费上下文，保证视觉调用按同一 token 与分组计费
		// （WriteContext 仅复制用户级字段，不复制 token 限额、token key、实际使用分组等）
		for _, k := range []constant.ContextKey{
			constant.ContextKeyTokenId,
			constant.ContextKeyTokenKey,
			constant.ContextKeyTokenUnlimited,
			constant.ContextKeyTokenGroup,
			constant.ContextKeyTokenCrossGroupRetry,
			constant.ContextKeyTokenSpecificChannelId,
			constant.ContextKeyTokenModelLimitEnabled,
			constant.ContextKeyTokenModelLimit,
			constant.ContextKeyUsingGroup,
			constant.ContextKeyAutoGroup,
			constant.ContextKeyAutoGroupIndex,
			constant.ContextKeyAutoGroupRetryIndex,
			constant.ContextKeyUserQuota,
			constant.ContextKeyUserStatus,
			constant.ContextKeyUserEmail,
			constant.ContextKeyUserName,
			constant.ContextKeyUserSetting,
		} {
			if v, exists := common.GetContextKey(c, k); exists {
				common.SetContextKey(visionCtx, k, v)
			}
		}

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

		// 6.1 独立子计费生命周期：估算价格 → 预扣费（遵循用户计费偏好，含订阅）
		// 与主请求解耦，视觉调用作为独立 BillingSession 结算，避免重复计费
		meta := &types.TokenCountMeta{
			MaxTokens: int(lo.FromPtrOr(request.MaxTokens, uint(0))),
		}
		estTokens := service.CountTextToken(promptText, config.VisionModelName)
		info.SetEstimatePromptTokens(estTokens)
		priceData, priceErr := helper.ModelPriceHelper(visionCtx, info, estTokens, meta)
		if priceErr != nil {
			return nil, fmt.Errorf("failed to calculate vision model price: %w", priceErr)
		}
		info.PriceData = priceData
		if !priceData.FreeModel {
			if apiErr := service.PreConsumeBilling(visionCtx, priceData.QuotaToPreConsume, info); apiErr != nil {
				return nil, fmt.Errorf("pre-consume billing failed for vision model '%s': %v", config.VisionModelName, apiErr)
			}
			// 请求失败或未产生结算时自动退款（成功结算后 NeedsRefund 返回 false，不重复退款）
			defer func() {
				if info.Billing != nil && info.Billing.NeedsRefund() {
					info.Billing.Refund(visionCtx)
				}
			}()
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

	// 存入 L4 pHash 缓存（跨请求模糊匹配，限同用户+模型+prompt）
	if !shared && phash != nil && config.PhashThreshold > 0 {
		phashCache.store(userId, config.VisionModelName, promptHash, *phash, description)
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

// hashPrompt 对 prompt 模板做完整哈希（不截断，避免不同 prompt 碰撞）
func hashPrompt(promptTemplate string) string {
	h := md5.Sum([]byte(promptTemplate))
	return hex.EncodeToString(h[:])
}

// buildCacheKey 构造 L2/L3 缓存键：用户 + 视觉模型 + prompt 哈希 + 完整图片哈希。
// 不做前缀截断，避免不同图片/不同 prompt 碰撞；包含用户与模型维度，杜绝跨用户串用。
func buildCacheKey(userId int, imageURL string, config dto.VisionUserSetting) string {
	imgHash := md5.Sum([]byte(imageURL))
	promptHash := md5.Sum([]byte(config.PromptTemplate))
	return fmt.Sprintf("%d|%s|%s|%s", userId, config.VisionModelName, hex.EncodeToString(promptHash[:]), hex.EncodeToString(imgHash[:]))
}

// storeInRequestCache 将描述存入请求级缓存，同一请求中相同 URL 直接复用
// 使用互斥锁保护 map，避免多图并行分析时的并发写入
func storeInRequestCache(requestID string, imageURL string, desc string) {
	if requestID == "" {
		return
	}
	entry, found, _ := requestCache.Get(requestID)
	if !found || entry == nil {
		// 串行化创建：并发 miss 时只创建一个 entry，避免互相覆盖丢失内容
		requestCacheInitMu.Lock()
		entry, found, _ = requestCache.Get(requestID)
		if !found || entry == nil {
			entry = &requestCacheEntry{m: make(map[string]string)}
			requestCache.Set(requestID, entry)
		}
		requestCacheInitMu.Unlock()
	}
	entry.mu.Lock()
	entry.m[imageURL] = desc
	entry.mu.Unlock()
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

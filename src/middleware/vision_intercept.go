package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/service/vision"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/sync/errgroup"
)

// VisionIntercept 视觉拦截中间件
func VisionIntercept() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从用户设置读取视觉配置（用户级）
		userSetting, exists := common.GetContextKeyType[dto.UserSetting](c, constant.ContextKeyUserSetting)
		if !exists || userSetting.Vision == nil || !userSetting.Vision.Enabled || userSetting.Vision.VisionSuffix == "" {
			c.Next()
			return
		}
		settings := userSetting.Vision

		if !strings.HasPrefix(c.Request.Header.Get("Content-Type"), "application/json") {
			c.Next()
			return
		}

		storage, err := common.GetBodyStorage(c)
		if err != nil {
			c.Next()
			return
		}

		bodyBytes, err := storage.Bytes()
		if err != nil {
			c.Next()
			return
		}

		modelValue := gjson.GetBytes(bodyBytes, "model")
		if !modelValue.Exists() || modelValue.Type != gjson.String {
			c.Next()
			return
		}

		model := modelValue.String()
		if !strings.HasSuffix(model, settings.VisionSuffix) {
			c.Next()
			return
		}

		logger.LogInfo(c, fmt.Sprintf("[vision] intercepting model=%s suffix=%s", model, settings.VisionSuffix))
		strippedModel := strings.TrimSuffix(model, settings.VisionSuffix)

		// 无消息 → 仅去后缀
		messagesRaw := gjson.GetBytes(bodyBytes, "messages")
		if !messagesRaw.Exists() || !messagesRaw.IsArray() {
			stripSuffixAndProceed(c, bodyBytes, strippedModel, false, settings)
			return
		}

		isClaudeFormat := isClaudeMessageFormat(messagesRaw)

		var images []vision.ImageBlock
		var extractErr error
		if isClaudeFormat {
			images, extractErr = extractClaudeImages(bodyBytes)
		} else {
			images = extractOpenAIImages(bodyBytes)
		}
		if extractErr != nil {
			abortWithVisionError(c, http.StatusInternalServerError, "failed to parse message images: "+extractErr.Error())
			return
		}

		// 找到最后一条 user 消息索引，用于区分新旧图片
		lastUserIdx := -1
		{
			messages := messagesRaw.Array()
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Get("role").String() == "user" {
					lastUserIdx = i
					break
				}
			}
		}

		// 无图片 → 仅去后缀
		if len(images) == 0 {
			logger.LogInfo(c, fmt.Sprintf("[vision] no images found in %d messages", len(messagesRaw.Array())))
			stripSuffixAndProceed(c, bodyBytes, strippedModel, false, settings)
			return
		}

		// 去重：相同 URL 只处理一次
		seen := make(map[string]int)         // imageURL → first deduped index
		dedupMap := make([]int, len(images)) // original → deduped index
		for i := range dedupMap {
			dedupMap[i] = -1
		}
		dedupedImages := make([]vision.ImageBlock, 0, len(images))
		for i, img := range images {
			if firstIdx, exists := seen[img.ImageURL]; exists {
				dedupMap[i] = firstIdx
			} else {
				idx := len(dedupedImages)
				seen[img.ImageURL] = idx
				dedupedImages = append(dedupedImages, img)
				dedupMap[i] = idx
			}
		}

		// Phase A: 并行计算 pHash（CPU 密集型，信号量限制 8 并发）
		requestID := c.GetString(common.RequestIdKey)

		type phashResult struct {
			hash uint64
			err  error
		}
		dedupedPhashes := make([]phashResult, len(dedupedImages))
		{
			var wg sync.WaitGroup
			sem := make(chan struct{}, 8)
			for i := range dedupedImages {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()

					img, err := vision.DecodeBase64Image(dedupedImages[idx].ImageURL)
					if err != nil {
						dedupedPhashes[idx] = phashResult{err: err}
						return
					}
					hash, err := vision.ComputePhash(img)
					dedupedPhashes[idx] = phashResult{hash: hash, err: err}
				}(i)
			}
			wg.Wait()
		}

		// Phase B: 贪心聚类 —— 汉明距离 ≤ threshold 的图片视为同一组
		type phashGroup struct {
			members  []int   // dedupedImages 索引
			repPhash *uint64 // 代表哈希（nil = 降级）
		}
		groups := make([]phashGroup, 0)
		dedupToGroup := make([]int, len(dedupedImages))

		if settings.PhashThreshold > 0 {
			for i, pr := range dedupedPhashes {
				if pr.err != nil {
					// 解码/计算失败 → 独立组
					dedupToGroup[i] = len(groups)
					groups = append(groups, phashGroup{members: []int{i}, repPhash: nil})
					continue
				}
				found := false
				for g, group := range groups {
					if group.repPhash == nil {
						continue
					}
					if vision.HammingDistance(pr.hash, *group.repPhash) <= settings.PhashThreshold {
						groups[g].members = append(groups[g].members, i)
						dedupToGroup[i] = g
						found = true
						break
					}
				}
				if !found {
					h := pr.hash
					dedupToGroup[i] = len(groups)
					groups = append(groups, phashGroup{members: []int{i}, repPhash: &h})
				}
			}
		} else {
			// 阈值=0：禁用 pHash，每个唯一图独立成组
			for i := range dedupedImages {
				groups = append(groups, phashGroup{members: []int{i}, repPhash: nil})
				dedupToGroup[i] = i
			}
		}

		phashGroupedCount := 0
		if len(dedupedImages) > len(groups) {
			phashGroupedCount = len(dedupedImages) - len(groups)
		}

		// 日志：同时展示 URL 去重和 pHash 聚类效果
		dedupedCount := 0
		if len(images) > len(dedupedImages) {
			dedupedCount = len(images) - len(dedupedImages)
		}
		logger.LogInfo(c, fmt.Sprintf("[vision] found %d images (%d url-unique, %d url-deduped, %d phash-groups, %d phash-grouped), first at messages.%d.content.%d",
			len(images), len(dedupedImages), dedupedCount, len(groups), phashGroupedCount,
			images[0].MessageIdx, images[0].ContentIdx))

		// 预计算每组是否包含新图片（来自最后一条 user 消息）
		groupHasNew := make([]bool, len(groups))
		for gi, group := range groups {
			for _, mi := range group.members {
				if dedupedImages[mi].MessageIdx == lastUserIdx {
					groupHasNew[gi] = true
					break
				}
			}
		}

		// Phase C: 按组并行处理
		// - 新图组：完整 AnalyzeImage（缓存 + API 调用）
		// - 旧图组：仅查 L4/L2 缓存，无缓存则保留原图让模型直接查看
		groupDescriptions := make([]string, len(groups))
		groupErrors := make([]error, len(groups))
		groupCached := make([]bool, len(groups))
		g, gctx := errgroup.WithContext(c.Request.Context())
		g.SetLimit(4)

		for gi := range groups {
			gi := gi
			g.Go(func() error {
				rep := groups[gi].members[0]
				if groupHasNew[gi] {
					ctx, cancel := context.WithTimeout(gctx, 30*time.Second)
					defer cancel()
					desc, cached, err := vision.AnalyzeImage(c, ctx, *settings, dedupedImages[rep].ImageURL, requestID, groups[gi].repPhash)
					if err != nil {
						groupErrors[gi] = err
						return nil
					}
					groupDescriptions[gi] = desc
					groupCached[gi] = cached
				} else {
					desc, found := vision.LookupCachedDescription(dedupedImages[rep].ImageURL, *settings, groups[gi].repPhash)
					if found {
						groupDescriptions[gi] = desc
						groupCached[gi] = true
					} else {
						// 旧图无缓存：占位文本替代，既不调 API 也避免上游报错
						groupDescriptions[gi] = "[Image]"
						groupCached[gi] = true
					}
				}
				return nil
			})
		}

		g.Wait()

		// 统计
		cachedCount := 0
		apiCallCount := 0
		for gi := range groups {
			if groupCached[gi] {
				cachedCount++
			} else {
				apiCallCount++
			}
		}
		logger.LogInfo(c, fmt.Sprintf("[vision] %d groups: %d cached, %d api-call",
			len(groups), cachedCount, apiCallCount))

		// 展开组结果到 dedupedImages 级别
		dedupedDescriptions := make([]string, len(dedupedImages))
		dedupedErrors := make([]error, len(dedupedImages))
		for gi, group := range groups {
			for _, mi := range group.members {
				dedupedDescriptions[mi] = groupDescriptions[gi]
				dedupedErrors[mi] = groupErrors[gi]
			}
		}

		// 展开去重结果到原始 images 数组
		descriptions := make([]string, len(images))
		failedCount := 0
		var firstError error
		for i := range images {
			di := dedupMap[i]
			if dedupedErrors[di] != nil {
				// 分析失败：返回错误，不再静默替换为占位符
				failedCount++
				if firstError == nil {
					firstError = dedupedErrors[di]
				}
				logger.LogInfo(c, fmt.Sprintf("[vision] image %d (url=%.100s...) failed: %v",
					i, images[i].ImageURL, dedupedErrors[di]))
			} else {
				descriptions[i] = dedupedDescriptions[di]
			}
		}

		// 有图片分析失败时，返回错误给客户端
		if failedCount > 0 {
			abortWithVisionError(c, http.StatusBadGateway, fmt.Sprintf("image analysis failed for %d image(s): %v", failedCount, firstError))
			return
		}

		// 替换所有图片为文字描述
		replacedCount := 0
		modified := bodyBytes
		for i := len(images) - 1; i >= 0; i-- {
			if descriptions[i] == "" {
				continue
			}
			img := images[i]
			replacementText := "[Image description: " + descriptions[i] + "]"
			contentPath := img.NestedPath
			if contentPath == "" {
				contentPath = "messages." + strconv.Itoa(img.MessageIdx) + ".content." + strconv.Itoa(img.ContentIdx)
			}

			replacement := dto.MediaContent{
				Type: dto.ContentTypeText,
				Text: replacementText,
			}
			replacementJSON, err := json.Marshal(replacement)
			if err != nil {
				abortWithVisionError(c, http.StatusInternalServerError, "failed to marshal replacement content")
				return
			}

			var sjsonErr error
			modified, sjsonErr = sjson.SetRawBytes(modified, contentPath, replacementJSON)
			if sjsonErr != nil {
				abortWithVisionError(c, http.StatusInternalServerError, "failed to replace image with description")
				return
			}
			replacedCount++
		}
		bodyBytes = modified

		logger.LogInfo(c, fmt.Sprintf("[vision] replaced %d/%d images (%d failed)", replacedCount, len(images), failedCount))

		stripSuffixAndProceed(c, bodyBytes, strippedModel, true, settings)
	}
}

// stripSuffixAndProceed 设置模型名并继续——三个退出点的公共逻辑
func stripSuffixAndProceed(c *gin.Context, body []byte, strippedModel string, markIntercepted bool, settings *dto.VisionUserSetting) {
	modified, err := sjson.SetBytes(body, "model", strippedModel)
	if err != nil {
		abortWithVisionError(c, http.StatusInternalServerError, "failed to modify model name")
		return
	}
	replaceBody(c, modified)
	if markIntercepted {
		c.Set("vision_intercepted", true)
	}
	c.Next()
}

func isClaudeMessageFormat(messagesRaw gjson.Result) bool {
	for _, msg := range messagesRaw.Array() {
		content := msg.Get("content")
		if content.IsArray() && hasClaudeImageRecursive(content) {
			return true
		}
	}
	return false
}

func hasClaudeImageRecursive(content gjson.Result) bool {
	for _, item := range content.Array() {
		if item.Get("type").String() == "image" && item.Get("source").Exists() {
			return true
		}
		nested := item.Get("content")
		if nested.IsArray() && hasClaudeImageRecursive(nested) {
			return true
		}
	}
	return false
}

func extractOpenAIImages(bodyBytes []byte) []vision.ImageBlock {
	var images []vision.ImageBlock
	messages := gjson.GetBytes(bodyBytes, "messages").Array()

	for msgIdx, msg := range messages {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for contentIdx, item := range content.Array() {
			switch item.Get("type").String() {
			case dto.ContentTypeImageURL:
				imgURL := extractImageOrVideoURL(item, "image_url")
				if imgURL != "" {
					images = append(images, vision.ImageBlock{
						MessageIdx: msgIdx,
						ContentIdx: contentIdx,
						ImageURL:   imgURL,
					})
				}
			case dto.ContentTypeVideoUrl:
				videoURL := extractImageOrVideoURL(item, "video_url")
				if videoURL != "" {
					images = append(images, vision.ImageBlock{
						MessageIdx: msgIdx,
						ContentIdx: contentIdx,
						ImageURL:   videoURL,
					})
				}
			}
		}
	}
	return images
}

// extractImageOrVideoURL 统一处理 image_url / video_url 的字符串和对象格式
func extractImageOrVideoURL(item gjson.Result, field string) string {
	val := item.Get(field)
	if val.IsObject() {
		return val.Get("url").String()
	}
	return val.String()
}

func extractClaudeImages(bodyBytes []byte) ([]vision.ImageBlock, error) {
	var images []vision.ImageBlock
	messages := gjson.GetBytes(bodyBytes, "messages").Array()
	for msgIdx, msg := range messages {
		content := msg.Get("content")
		if content.IsArray() {
			extractClaudeImagesRecursive(content, msgIdx, "", &images)
		}
	}
	return images, nil
}

func extractClaudeImagesRecursive(content gjson.Result, msgIdx int, pathPrefix string, images *[]vision.ImageBlock) {
	for ci, item := range content.Array() {
		itemType := item.Get("type").String()

		var currentPath string
		if pathPrefix == "" {
			currentPath = fmt.Sprintf("messages.%d.content.%d", msgIdx, ci)
		} else {
			currentPath = fmt.Sprintf("%s.content.%d", pathPrefix, ci)
		}

		if itemType == "image" {
			source := item.Get("source")
			if source.Exists() {
				imgURL := source.Get("url").String()
				mediaType := source.Get("media_type").String()
				if imgURL == "" {
					data := source.Get("data").String()
					if data != "" {
						if mediaType == "" {
							mediaType = "image/png"
						}
						imgURL = "data:" + mediaType + ";base64," + data
					}
				}
				if imgURL != "" {
					*images = append(*images, vision.ImageBlock{
						MessageIdx: msgIdx,
						ContentIdx: ci,
						NestedPath: currentPath,
						ImageURL:   imgURL,
						MediaType:  mediaType,
					})
				}
			}
		}

		nestedContent := item.Get("content")
		if nestedContent.IsArray() {
			extractClaudeImagesRecursive(nestedContent, msgIdx, currentPath, images)
		}
	}
}

// replaceBody 替换 Gin 上下文中的请求体，先关闭旧存储防泄露
func replaceBody(c *gin.Context, newBody []byte) {
	// 关闭旧存储（防 diskStorage 临时文件泄露）
	if old, exists := c.Get(common.KeyBodyStorage); exists {
		if oldBS, ok := old.(common.BodyStorage); ok {
			oldBS.Close()
		}
	}

	newStorage, err := common.CreateBodyStorage(newBody)
	if err != nil {
		c.Request.Body = io.NopCloser(bytes.NewReader(newBody))
		return
	}
	c.Set(common.KeyBodyStorage, newStorage)
	c.Request.Body = io.NopCloser(newStorage)
}

func abortWithVisionError(c *gin.Context, statusCode int, message string) {
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": "Vision interception failed: " + message,
			"type":    "vision_error",
		},
	})
	c.Abort()
}


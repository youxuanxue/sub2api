package service

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// geminiImageOutputCounterKey 是请求级图片计数器挂在 gin.Context 上的键。
const geminiImageOutputCounterKey = "gemini_image_output_counter"

// geminiImageOutputCounter tracks actual validated images, deduplicating repeated
// cumulative stream chunks while retaining distinct incremental images.
type geminiImageOutputCounter = geminiImageMultiplicity

// beginGeminiImageOutputObservation 在每次 Forward 开头重置计数器。
// failover 会拿同一个 gin.Context 重跑转发，不重置就会把上一个账号的图数带进来。
func beginGeminiImageOutputObservation(c *gin.Context) *geminiImageOutputCounter {
	if c == nil {
		return nil
	}
	counter := newGeminiImageMultiplicity()
	c.Set(geminiImageOutputCounterKey, counter)
	return counter
}

func geminiImageOutputCounterFromContext(c *gin.Context) *geminiImageOutputCounter {
	if c == nil {
		return nil
	}
	value, ok := c.Get(geminiImageOutputCounterKey)
	if !ok {
		return nil
	}
	counter, _ := value.(*geminiImageOutputCounter)
	return counter
}

// observeGeminiImageOutputs 观测一段上游响应（整份或单个 chunk）里的图片。
// 调用点与 upstreamResponseModelObserver.ObserveGemini 一一对应——那里拿得到
// 解包后的上游响应体，这里需要的是同一份字节。
func observeGeminiImageOutputs(c *gin.Context, payload []byte) {
	counter := geminiImageOutputCounterFromContext(c)
	if counter == nil {
		return
	}
	counter.observe(geminiInlineImageOutputs(payload))
}

func observedGeminiImageOutputs(c *gin.Context) int {
	counter := geminiImageOutputCounterFromContext(c)
	if counter == nil {
		return 0
	}
	return counter.count
}

// Model names describe capability, not delivered output. Text-only and blocked
// responses never incur an image charge.
func resolveGeminiImageCount(c *gin.Context, _, _ string) int {
	return observedGeminiImageOutputs(c)
}

// geminiInlineImageOutputs reads validated camelCase and snake_case image parts.
// Upstream 917dbdc70: referenced fileData / file_data images are billable too;
// they are keyed by MIME type + URI so cumulative stream chunks dedupe.
func geminiInlineImageOutputs(payload []byte) []string {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return nil
	}
	var images []string
	gjson.GetBytes(payload, "candidates").ForEach(func(_, candidate gjson.Result) bool {
		candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			var decoded map[string]any
			if json.Unmarshal([]byte(part.Raw), &decoded) == nil {
				if image, ok := geminiInlineImageMarkdown(decoded); ok {
					images = append(images, image)
					return true
				}
			}
			// 同一个 part 最多计费一次：inline 已命中时不再看 fileData。
			if image, ok := geminiFileImageReference(part); ok {
				images = append(images, image)
			}
			return true
		})
		return true
	})
	return images
}

// geminiFileImageReference returns a stable identity for an image fileData part
// that carries a non-empty URI.
func geminiFileImageReference(part gjson.Result) (string, bool) {
	for _, key := range []string{"fileData", "file_data"} {
		image := part.Get(key)
		if !image.Exists() {
			continue
		}
		mimeType := image.Get("mimeType")
		if !mimeType.Exists() {
			mimeType = image.Get("mime_type")
		}
		normalizedMIME := strings.ToLower(strings.TrimSpace(mimeType.String()))
		if !isGeminiInlineImageMIMEType(normalizedMIME) {
			continue
		}
		uri := image.Get("fileUri")
		if !uri.Exists() {
			uri = image.Get("file_uri")
		}
		if uri.Type == gjson.String && strings.TrimSpace(uri.String()) != "" {
			return "file:" + normalizedMIME + ":" + strings.TrimSpace(uri.String()), true
		}
	}
	return "", false
}

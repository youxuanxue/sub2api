package service

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// geminiImageOutputCounterKey 是请求级内联图片计数器挂在 gin.Context 上的键。
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

// observeGeminiImageOutputs 观测一段上游响应（整份或单个 chunk）里的内联图片。
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
				}
			}
			return true
		})
		return true
	})
	return images
}

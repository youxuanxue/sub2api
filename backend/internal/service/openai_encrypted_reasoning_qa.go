package service

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openaiEncryptedReasoningGinKey = "ops_openai_encrypted_reasoning"

// stashOpenAIEncryptedReasoningFromSSE 从 Responses SSE/JSON 抽出 reasoning.encrypted_content，
// 写入 gin context 供 QA blob response.encrypted_reasoning 记录。
func stashOpenAIEncryptedReasoningFromSSE(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 || !gjson.ValidBytes(data) {
		return
	}
	if item := gjson.GetBytes(data, "item"); item.Exists() {
		stashOpenAIEncryptedReasoningItem(c, item)
	}
	if output := gjson.GetBytes(data, "response.output"); output.IsArray() {
		output.ForEach(func(_, item gjson.Result) bool {
			stashOpenAIEncryptedReasoningItem(c, item)
			return true
		})
		return
	}
	if output := gjson.GetBytes(data, "output"); output.IsArray() {
		output.ForEach(func(_, item gjson.Result) bool {
			stashOpenAIEncryptedReasoningItem(c, item)
			return true
		})
	}
}

func stashOpenAIEncryptedReasoningItem(c *gin.Context, item gjson.Result) {
	if item.Get("type").String() != "reasoning" {
		return
	}
	enc := strings.TrimSpace(item.Get("encrypted_content").String())
	if enc == "" {
		return
	}
	id := strings.TrimSpace(item.Get("id").String())
	raw, err := json.Marshal(map[string]string{
		"item_id":           id,
		"encrypted_content": enc,
	})
	if err != nil {
		return
	}
	block := string(raw)
	existing, _ := c.Get(openaiEncryptedReasoningGinKey)
	prior, _ := existing.([]string)
	for _, seen := range prior {
		if seen == block {
			return
		}
	}
	c.Set(openaiEncryptedReasoningGinKey, append(append([]string{}, prior...), block))
}

package service

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openaiEncryptedReasoningGinKey = "ops_openai_encrypted_reasoning"

// observeOpenAIResponsesEvent 是 QA encrypted_reasoning 的唯一生产入口。
// 所有会把 OpenAI Responses JSON 事件写给客户端的路径都必须走这里，
// 禁止在各转发循环里再手写 stash。
func observeOpenAIResponsesEvent(c *gin.Context, data []byte) {
	stashOpenAIEncryptedReasoningFromSSE(c, data)
}

func observeOpenAIResponsesSSEBody(c *gin.Context, body string) {
	if strings.TrimSpace(body) == "" {
		return
	}
	if bodyHasSSEFraming([]byte(body)) {
		forEachOpenAISSEDataPayload(body, func(data []byte) {
			observeOpenAIResponsesEvent(c, data)
		})
		return
	}
	observeOpenAIResponsesEvent(c, []byte(body))
}

// stashOpenAIEncryptedReasoningFromSSE 从 Responses SSE/JSON 抽出 reasoning.encrypted_content，
// 写入 gin context 供 QA blob response.encrypted_reasoning 记录。
// 生产代码必须走 observeOpenAIResponsesEvent，不要直接调用。
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

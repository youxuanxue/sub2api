package service

import (
	"encoding/json"
	"fmt"
)

// wrapAntigravityCompatGeminiRequest preserves the legacy compatibility
// envelope. Native Gemini requests need a sessionId, while the Chat
// Completions compatibility route intentionally keeps the converted payload
// stateless for its existing wire contract.
func (s *AntigravityGatewayService) wrapAntigravityCompatGeminiRequest(projectID, model string, body []byte) ([]byte, error) {
	wrapped, err := s.wrapV1InternalRequest(projectID, model, body)
	if err != nil {
		return nil, err
	}
	var envelope map[string]any
	if err := json.Unmarshal(wrapped, &envelope); err != nil {
		return nil, fmt.Errorf("解析兼容请求包装失败: %w", err)
	}
	if request, ok := envelope["request"].(map[string]any); ok {
		delete(request, "sessionId")
	}
	return json.Marshal(envelope)
}

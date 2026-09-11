package service

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Honour an explicit capacity rule for a known model, including failures
// carried inside HTTP 200 SSE. Without a rule, capacity remains request-scoped.
func (s *OpenAIGatewayService) tkHandleOpenAIStreamCapacityRule(c *gin.Context, account *Account, payload []byte, message string, canonicalModel ...string) bool {
	if s == nil || s.rateLimitService == nil || account == nil || account.Platform != PlatformOpenAI || !account.IsTempUnschedulableEnabled() {
		return false
	}
	model := firstNonEmpty(canonicalModel...)
	if model == "" {
		return false
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	// response.failed may echo instructions/output; only the actual error
	// envelope may match an operator keyword and affect model availability.
	body := openAIStreamFailedEventPassthroughBody(payload, message)
	fields := gjson.GetBytes(body, "error")
	body, _ = json.Marshal(map[string]any{"error": map[string]string{
		"code": fields.Get("code").String(), "type": fields.Get("type").String(), "message": fields.Get("message").String(),
	}})
	return s.rateLimitService.HandleTempUnschedulable(stateCtx, account, http.StatusServiceUnavailable, body, model)
}

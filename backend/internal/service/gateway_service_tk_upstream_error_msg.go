package service

// TokenKey: service-layer counterpart of handler.TkEnrichForbiddenMessage.
//
// Rule §5 (CLAUDE.md): four service-layer error paths emit the literal
// "Upstream access forbidden, please contact administrator" today —
//
//   - gateway_service.go:handleErrorResponse (case 403)
//   - openai_gateway_service.go:handleErrorResponse (case 403)
//   - gemini_messages_compat_service.go:writeGeminiMappedError (case 403)
//   - antigravity_gateway_service.go:writeMappedClaudeError (case 403)
//
// Each receives gin.Context and can read OpsModelKey + OpsRequestBodyKey to
// produce an actionable hint (body bytes + model + /compact suggestion).
//
// This helper is intentionally duplicated from handler.TkEnrichForbiddenMessage
// to avoid a service→handler import cycle (layer rule §8). The wire contract
// is the literal Gin-context key strings declared in ops_upstream_context.go.
//
// Why we don't blanket-replace the default: the "contact administrator"
// suffix remains correct for true OAuth/credential 403s with small bodies;
// enrichment only fires when the context carries enough metadata.

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// TkEnrichForbiddenMessage rewrites the upstream-403 default message into a
// body-size-aware, actionable hint when the gin context carries a non-empty
// request body and/or a resolved model name. Falls back to defaultMsg
// verbatim otherwise.
//
// nil c → defaultMsg. Empty body + empty model → defaultMsg.
func TkEnrichForbiddenMessage(c *gin.Context, defaultMsg string) string {
	if c == nil {
		return defaultMsg
	}
	model := readOpsModel(c)
	bodyLen := readOpsRequestBodyLen(c)
	if model == "" && bodyLen <= 0 {
		return defaultMsg
	}

	var detail string
	switch {
	case bodyLen > 0 && model != "":
		detail = fmt.Sprintf("Request body %d bytes for model %q was rejected by upstream. ", bodyLen, model)
	case bodyLen > 0:
		detail = fmt.Sprintf("Request body %d bytes was rejected by upstream. ", bodyLen)
	default:
		detail = fmt.Sprintf("Model %q was rejected by upstream. ", model)
	}
	return "Upstream returned 403 for this request. " + detail +
		"If body is large, run /compact or start a new conversation to reduce it; otherwise contact administrator."
}

func readOpsModel(c *gin.Context) string {
	v, ok := c.Get(OpsModelKey)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func readOpsRequestBodyLen(c *gin.Context) int {
	v, ok := c.Get(OpsRequestBodyKey)
	if !ok {
		return 0
	}
	raw, _ := v.([]byte)
	return len(raw)
}

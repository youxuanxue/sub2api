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
	return "Upstream returned 403 for this request. " + detail + tkForbiddenAdvice(bodyLen)
}

// tkForbiddenCompactHintThreshold is the request-body size (bytes) at/above
// which the "run /compact" advice is plausibly relevant. Below it, an upstream
// 403 is almost never about request size: Anthropic's real size ceiling is
// ~32 MB and surfaces as 413, not 403. A small-body 403 is an upstream
// access/policy rejection — edge auth guard (e.g. canonical claude-cli-only
// path rejecting a non-cc client), WAF / datacenter-IP / fingerprint, or
// credential scope — none of which /compact can fix. Suggesting /compact there
// sends the caller chasing the wrong cause and fossilizes "body size" as the
// explanation (exactly the failure mode behind the removed byte soft-gate, ops
// memory "upstream_byte_403_is_waf_not_size_limit").
const tkForbiddenCompactHintThreshold = 1 << 20 // 1 MiB

// tkForbiddenAdvice returns the actionable tail of the 403 message. Only a
// genuinely large body gets the /compact suggestion; otherwise we say the
// rejection is an upstream access/policy decision unrelated to size, so the
// caller retries / escalates instead of uselessly shrinking an already-tiny
// request.
func tkForbiddenAdvice(bodyLen int) string {
	if bodyLen >= tkForbiddenCompactHintThreshold {
		return "If body is large, run /compact or start a new conversation to reduce it; otherwise contact administrator."
	}
	return "This is an upstream access/policy rejection unrelated to request size; retry the request, and contact administrator if it persists."
}

// TkEnrichClaudeIncidentMessage rewrites a failover-exhausted client message
// into an incident-aware notice when status.claude.com reports a non-operational
// Claude API. The goal is to redirect the caller's attention to the real
// upstream (Anthropic) instead of the generic "all accounts exhausted" wording,
// which wrongly implicates the TokenKey account pool or the user's own key.
//
// Returns defaultMsg verbatim when there is no active incident (including the
// staleness fail-safe in IsClaudeAPIIncident), so non-incident behaviour is
// unchanged. upstreamStatusCode carries the upstream HTTP status into the
// message so the caller can see what Anthropic actually returned.
func TkEnrichClaudeIncidentMessage(defaultMsg string, upstreamStatusCode int) string {
	if !IsClaudeAPIIncident() {
		return defaultMsg
	}
	snap := GetClaudeStatusSnapshot()
	return fmt.Sprintf(
		"Anthropic upstream is currently reporting an incident (Claude API status: %s, upstream returned %d). "+
			"This is an Anthropic-side outage, not a problem with your account or API key. "+
			"Check live status at %s — requests recover automatically once Anthropic returns to operational.",
		snap.Status, upstreamStatusCode, claudeStatusPageURL,
	)
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

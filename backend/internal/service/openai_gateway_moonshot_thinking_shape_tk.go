package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIInvalidParameterError is a gateway-side request validation failure that
// should surface to clients as an OpenAI-shaped 400 with error.param set so
// SDKs and operators can fix the exact field without reading opaque upstream
// English dumps.
type OpenAIInvalidParameterError struct {
	Param   string
	Code    string
	Message string
}

func (e *OpenAIInvalidParameterError) Error() string {
	if e == nil {
		return ""
	}
	if e.Param == "" {
		return e.Message
	}
	return fmt.Sprintf("%s (param=%s)", e.Message, e.Param)
}

func newOpenAIInvalidParameterError(param, message string) *OpenAIInvalidParameterError {
	return &OpenAIInvalidParameterError{
		Param:   param,
		Code:    "invalid_parameter",
		Message: message,
	}
}

func writeOpenAIInvalidParameterError(c *gin.Context, err *OpenAIInvalidParameterError) {
	if c == nil || err == nil {
		return
	}
	MarkResponseCommitted(c)
	payload := gin.H{
		"type":    "invalid_request_error",
		"code":    err.Code,
		"message": err.Message,
	}
	if strings.TrimSpace(err.Code) == "" {
		payload["code"] = "invalid_parameter"
	}
	if param := strings.TrimSpace(err.Param); param != "" {
		payload["param"] = param
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": payload})
}

type moonshotThinkingFamily int

const (
	moonshotThinkingNone moonshotThinkingFamily = iota
	moonshotThinkingK3
	moonshotThinkingK27Code
	moonshotThinkingK26
	moonshotThinkingK25
	moonshotThinkingKimiOther
)

// applyMoonshotThinkingShape normalizes Moonshot/Kimi thinking-related fields
// before upstream relay. On reject it writes a field-level OpenAI 400 (when c is
// non-nil) and returns *OpenAIInvalidParameterError so callers stop failover.
func applyMoonshotThinkingShape(c *gin.Context, model string, body []byte) ([]byte, error) {
	shaped, err := NormalizeMoonshotThinking(model, body)
	if err == nil {
		return shaped, nil
	}
	var inv *OpenAIInvalidParameterError
	if errors.As(err, &inv) && c != nil {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		writeOpenAIInvalidParameterError(c, inv)
	}
	return body, err
}

// NormalizeMoonshotThinking coerces common SDK mistakes onto Moonshot's
// per-model thinking contract and rejects shapes that cannot be safely rewritten.
//
// Contract (platform.kimi.ai):
//   - kimi-k3: no thinking; optional reasoning_effort low|high|max
//   - kimi-k2.7-code*: thinking always on; only enabled(+keep=all) or omit
//   - kimi-k2.6: thinking.type enabled|disabled; optional keep=all
//   - kimi-k2.5: thinking.type enabled|disabled; no keep / no reasoning_effort
//
// Boolean thinking (true/false) is rewritten to {"type":"enabled"|"disabled"}
// before family rules run — the primary prod 400 class.
func NormalizeMoonshotThinking(model string, body []byte) ([]byte, error) {
	family := resolveMoonshotThinkingFamily(model)
	if family == moonshotThinkingNone || len(body) == 0 {
		return body, nil
	}

	shaped := body
	var err error
	shaped, err = coerceMoonshotThinkingBool(shaped)
	if err != nil {
		return body, err
	}
	shaped = stripMoonshotThinkingBudgetTokens(shaped)
	shaped = promoteMoonshotNestedReasoningEffort(shaped)

	switch family {
	case moonshotThinkingK3:
		return normalizeMoonshotK3Thinking(shaped)
	case moonshotThinkingK27Code:
		return normalizeMoonshotK27Thinking(shaped)
	case moonshotThinkingK26:
		return normalizeMoonshotK26Thinking(shaped)
	case moonshotThinkingK25:
		return normalizeMoonshotK25Thinking(shaped)
	default:
		return shaped, nil
	}
}

func normalizeMoonshotModelID(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return ""
	}
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 {
		normalized = normalized[idx+1:]
	}
	return normalized
}

func resolveMoonshotThinkingFamily(model string) moonshotThinkingFamily {
	m := normalizeMoonshotModelID(model)
	if m == "" {
		return moonshotThinkingNone
	}
	if m == "kimi-k3" || strings.HasPrefix(m, "kimi-k3-") {
		return moonshotThinkingK3
	}
	if strings.Contains(m, "k2.7-code") {
		return moonshotThinkingK27Code
	}
	if m == "kimi-k2.6" || strings.HasPrefix(m, "kimi-k2.6-") {
		return moonshotThinkingK26
	}
	if m == "kimi-k2.5" || strings.HasPrefix(m, "kimi-k2.5-") {
		return moonshotThinkingK25
	}
	if strings.HasPrefix(m, "kimi-") || strings.HasPrefix(m, "moonshot-") {
		return moonshotThinkingKimiOther
	}
	return moonshotThinkingNone
}

func coerceMoonshotThinkingBool(body []byte) ([]byte, error) {
	thinking := gjson.GetBytes(body, "thinking")
	if !thinking.Exists() {
		return body, nil
	}
	switch thinking.Type {
	case gjson.True:
		next, err := sjson.SetBytes(body, "thinking", map[string]any{"type": "enabled"})
		if err != nil {
			return body, err
		}
		return next, nil
	case gjson.False:
		next, err := sjson.SetBytes(body, "thinking", map[string]any{"type": "disabled"})
		if err != nil {
			return body, err
		}
		return next, nil
	case gjson.Number:
		switch thinking.Num {
		case 1:
			next, err := sjson.SetBytes(body, "thinking", map[string]any{"type": "enabled"})
			if err != nil {
				return body, err
			}
			return next, nil
		case 0:
			next, err := sjson.SetBytes(body, "thinking", map[string]any{"type": "disabled"})
			if err != nil {
				return body, err
			}
			return next, nil
		default:
			return body, newOpenAIInvalidParameterError(
				"thinking",
				"thinking must be a boolean or an object {\"type\":\"enabled\"|\"disabled\"}",
			)
		}
	case gjson.String:
		return body, newOpenAIInvalidParameterError(
			"thinking",
			"thinking must be a boolean or an object {\"type\":\"enabled\"|\"disabled\"}",
		)
	case gjson.JSON:
		if thinking.IsObject() {
			return body, nil
		}
		return body, newOpenAIInvalidParameterError(
			"thinking",
			"thinking must be a boolean or an object {\"type\":\"enabled\"|\"disabled\"}",
		)
	case gjson.Null:
		next, err := sjson.DeleteBytes(body, "thinking")
		if err != nil {
			return body, err
		}
		return next, nil
	default:
		return body, newOpenAIInvalidParameterError(
			"thinking",
			"thinking must be a boolean or an object {\"type\":\"enabled\"|\"disabled\"}",
		)
	}
}

func stripMoonshotThinkingBudgetTokens(body []byte) []byte {
	if !gjson.GetBytes(body, "thinking.budget_tokens").Exists() {
		return body
	}
	next, err := sjson.DeleteBytes(body, "thinking.budget_tokens")
	if err != nil {
		return body
	}
	return next
}

func promoteMoonshotNestedReasoningEffort(body []byte) []byte {
	if gjson.GetBytes(body, "reasoning_effort").Exists() {
		return body
	}
	nested := strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())
	if nested == "" {
		return body
	}
	next, err := sjson.SetBytes(body, "reasoning_effort", nested)
	if err != nil {
		return body
	}
	if deleted, err := sjson.DeleteBytes(next, "reasoning.effort"); err == nil {
		next = deleted
	}
	// Drop empty reasoning object if nothing remains.
	if reasoning := gjson.GetBytes(next, "reasoning"); reasoning.Exists() && reasoning.IsObject() {
		empty := true
		reasoning.ForEach(func(_, _ gjson.Result) bool {
			empty = false
			return false
		})
		if empty {
			if deleted, err := sjson.DeleteBytes(next, "reasoning"); err == nil {
				next = deleted
			}
		}
	}
	return next
}

func normalizeMoonshotK3Thinking(body []byte) ([]byte, error) {
	shaped := body
	if gjson.GetBytes(shaped, "thinking").Exists() {
		thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(shaped, "thinking.type").String()))
		if thinkingType == "disabled" {
			return body, newOpenAIInvalidParameterError(
				"thinking",
				"kimi-k3 always reasons; remove thinking (cannot disable). Use reasoning_effort: low|high|max instead",
			)
		}
		next, err := sjson.DeleteBytes(shaped, "thinking")
		if err != nil {
			return body, err
		}
		shaped = next
	}
	if !gjson.GetBytes(shaped, "reasoning_effort").Exists() {
		return shaped, nil
	}
	effort := strings.ToLower(strings.TrimSpace(gjson.GetBytes(shaped, "reasoning_effort").String()))
	switch effort {
	case "low", "high", "max":
		return shaped, nil
	default:
		return body, newOpenAIInvalidParameterError(
			"reasoning_effort",
			"kimi-k3 reasoning_effort must be one of: low, high, max",
		)
	}
}

func normalizeMoonshotK27Thinking(body []byte) ([]byte, error) {
	shaped := stripMoonshotUnsupportedReasoningEffort(body)
	if !gjson.GetBytes(shaped, "thinking").Exists() {
		return shaped, nil
	}
	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(shaped, "thinking.type").String()))
	if thinkingType == "disabled" || thinkingType == "" {
		if thinkingType == "disabled" {
			return body, newOpenAIInvalidParameterError(
				"thinking",
				"kimi-k2.7-code always thinks; omit thinking or use {\"type\":\"enabled\",\"keep\":\"all\"}",
			)
		}
		return body, newOpenAIInvalidParameterError(
			"thinking",
			"kimi-k2.7-code thinking.type must be \"enabled\" when thinking is set",
		)
	}
	if thinkingType != "enabled" {
		return body, newOpenAIInvalidParameterError(
			"thinking",
			"kimi-k2.7-code thinking.type must be \"enabled\" when thinking is set",
		)
	}
	if keep := gjson.GetBytes(shaped, "thinking.keep"); keep.Exists() && keep.Type != gjson.Null {
		keepVal := strings.ToLower(strings.TrimSpace(keep.String()))
		if keepVal != "all" {
			return body, newOpenAIInvalidParameterError(
				"thinking",
				"kimi-k2.7-code thinking.keep must be \"all\" when set",
			)
		}
	}
	// Omit thinking: upstream treats omit as always-on preserved thinking.
	next, err := sjson.DeleteBytes(shaped, "thinking")
	if err != nil {
		return body, err
	}
	return next, nil
}

func normalizeMoonshotK26Thinking(body []byte) ([]byte, error) {
	shaped := stripMoonshotUnsupportedReasoningEffort(body)
	return normalizeMoonshotK2ThinkingObject(shaped, true)
}

func normalizeMoonshotK25Thinking(body []byte) ([]byte, error) {
	shaped := stripMoonshotUnsupportedReasoningEffort(body)
	if gjson.GetBytes(shaped, "thinking.keep").Exists() {
		next, err := sjson.DeleteBytes(shaped, "thinking.keep")
		if err != nil {
			return body, err
		}
		shaped = next
	}
	return normalizeMoonshotK2ThinkingObject(shaped, false)
}

func normalizeMoonshotK2ThinkingObject(body []byte, allowKeep bool) ([]byte, error) {
	if !gjson.GetBytes(body, "thinking").Exists() {
		return body, nil
	}
	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	switch thinkingType {
	case "enabled", "disabled":
	default:
		return body, newOpenAIInvalidParameterError(
			"thinking",
			"thinking.type must be \"enabled\" or \"disabled\"",
		)
	}
	if !allowKeep {
		return body, nil
	}
	keep := gjson.GetBytes(body, "thinking.keep")
	if !keep.Exists() || keep.Type == gjson.Null {
		return body, nil
	}
	keepVal := strings.ToLower(strings.TrimSpace(keep.String()))
	if keepVal != "all" {
		return body, newOpenAIInvalidParameterError(
			"thinking",
			"thinking.keep must be \"all\" or null when set on kimi-k2.6",
		)
	}
	return body, nil
}

func stripMoonshotUnsupportedReasoningEffort(body []byte) []byte {
	shaped := body
	if gjson.GetBytes(shaped, "reasoning_effort").Exists() {
		if next, err := sjson.DeleteBytes(shaped, "reasoning_effort"); err == nil {
			shaped = next
		}
	}
	if gjson.GetBytes(shaped, "reasoning.effort").Exists() {
		if next, err := sjson.DeleteBytes(shaped, "reasoning.effort"); err == nil {
			shaped = next
		}
	}
	return shaped
}

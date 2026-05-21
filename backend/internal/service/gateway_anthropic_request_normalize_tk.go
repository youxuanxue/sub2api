package service

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// TokenKey: Anthropic native /v1/messages request-body normalization.
//
// Fixes two recurring client mistakes that previously surfaced as upstream
// 400s (see edge-us1 / edge-uk1 incident on 2026-05-21):
//
//  1. tool_choice given as an OpenAI-style string. Anthropic rejects with
//     "tool_choice: Input should be an object". We map the three OpenAI legal
//     string values to their Anthropic object equivalent. Unknown strings are
//     left untouched so the upstream still surfaces the client bug.
//
//  2. thinking.type=enabled together with tool_choice.type IN ("any","tool").
//     Anthropic rejects with "Thinking may not be enabled when tool_choice
//     forces tool use." Per the product decision recorded in the incident
//     report (strategy A), we strip the thinking field — the client's
//     forced-tool-use intent wins.
//
// Scope: only the standard forward path (gateway_service.Forward — i.e. all
// Anthropic-platform requests EXCEPT IsAnthropicAPIKeyPassthroughEnabled and
// Bedrock, both of which early-return before this hook). The hook runs once
// per request before any other body rewrite so downstream code sees a
// well-formed body.
//
// Original client body is preserved: the ops_error_logs.request_body field is
// stashed by handler.setOpsRequestContext BEFORE this hook runs, so the
// pre-normalize body remains visible for debugging even when normalize fires.
//
// Observability:
//   - Every successful normalization emits an INFO log
//     "gateway.anthropic_request_normalized" with request_id + the list of
//     changes applied. Use this to count total normalize hits.
//   - Each change also appends an OpsUpstreamErrorEvent (kind="request_normalized")
//     to the request's ops_error_logs.upstream_errors array. Combined with the
//     INFO log this lets operators measure "save rate" = 1 - (rows-with-event
//     / total-normalize-hits).
//
// Disable at runtime:
//   UPDATE settings SET value='false'
//    WHERE key='tk_anthropic_request_normalize_enabled';

// tkAnthropicNormalizeChange identifies one normalization rule that was
// applied to a request body.
type tkAnthropicNormalizeChange string

const (
	tkNormalizeChangeToolChoiceStringToObject tkAnthropicNormalizeChange = "tool_choice_string_to_object"
	tkNormalizeChangeThinkingForcesToolUse    tkAnthropicNormalizeChange = "thinking_with_forces_tool_use"
)

// tkNormalizeAnthropicRequestBody applies request-body normalization for the
// Anthropic native /v1/messages path. Returns the possibly-modified body; the
// original is left untouched so callers that still need the raw payload (ops
// logging, debug dumps) can read it before calling this function.
//
// Safe to call with body == nil / empty; returns the input unchanged.
func (s *GatewayService) tkNormalizeAnthropicRequestBody(ctx context.Context, c *gin.Context, body []byte) []byte {
	if s == nil || s.settingService == nil || len(body) == 0 {
		return body
	}
	if !s.settingService.IsAnthropicRequestNormalizeEnabled(ctx) {
		return body
	}

	next := body
	var changes []tkAnthropicNormalizeChange

	if patched, applied := tkNormalizeAnthropicToolChoiceString(next); applied {
		next = patched
		changes = append(changes, tkNormalizeChangeToolChoiceStringToObject)
	}

	if patched, applied := tkNormalizeAnthropicThinkingForcesToolUse(next); applied {
		next = patched
		changes = append(changes, tkNormalizeChangeThinkingForcesToolUse)
	}

	if len(changes) == 0 {
		return next
	}

	tkLogAnthropicNormalize(ctx, changes)
	tkRecordAnthropicNormalizeOpsEvent(c, changes)
	return next
}

// tkNormalizeAnthropicToolChoiceString rewrites OpenAI-shaped string values
// for tool_choice into Anthropic's required object form. Unknown strings are
// preserved so the upstream surfaces the bug.
func tkNormalizeAnthropicToolChoiceString(body []byte) ([]byte, bool) {
	tc := gjson.GetBytes(body, "tool_choice")
	if !tc.Exists() || tc.Type != gjson.String {
		return body, false
	}

	var replacement map[string]any
	switch tc.String() {
	case "auto":
		replacement = map[string]any{"type": "auto"}
	case "required":
		// OpenAI "required" == Anthropic "any" (must call some tool, model
		// chooses which one).
		replacement = map[string]any{"type": "any"}
	case "none":
		replacement = map[string]any{"type": "none"}
	default:
		return body, false
	}

	out, err := sjson.SetBytes(body, "tool_choice", replacement)
	if err != nil {
		return body, false
	}
	return out, true
}

// tkNormalizeAnthropicThinkingForcesToolUse strips the thinking field when it
// conflicts with a tool_choice that forces tool use. Anthropic forbids
// thinking together with tool_choice.type IN ("any","tool"); we keep the
// forced-tool-use intent and drop thinking (strategy A).
func tkNormalizeAnthropicThinkingForcesToolUse(body []byte) ([]byte, bool) {
	thinking := gjson.GetBytes(body, "thinking")
	if !thinking.Exists() || thinking.Get("type").String() != "enabled" {
		return body, false
	}
	switch gjson.GetBytes(body, "tool_choice.type").String() {
	case "any", "tool":
		// fall through
	default:
		return body, false
	}
	out, err := sjson.DeleteBytes(body, "thinking")
	if err != nil {
		return body, false
	}
	return out, true
}

// tkLogAnthropicNormalize emits the INFO-level audit log. Keeping the log
// emit isolated makes it trivial to unit-test the pure rewrite functions
// above without touching slog.
func tkLogAnthropicNormalize(ctx context.Context, changes []tkAnthropicNormalizeChange) {
	if len(changes) == 0 {
		return
	}
	parts := make([]string, 0, len(changes))
	for _, ch := range changes {
		parts = append(parts, string(ch))
	}
	requestID, _ := ctx.Value(ctxkey.RequestID).(string)
	slog.Info("gateway.anthropic_request_normalized",
		slog.String("request_id", requestID),
		slog.String("changes", strings.Join(parts, ",")),
	)
}

// tkRecordAnthropicNormalizeOpsEvent records one ops upstream-errors event
// per request so an operator can SQL the rows that were normalized but still
// failed. The event only persists if the request ultimately errors (ops
// logger only writes rows for non-2xx final outcomes), so this measures
// "normalize did not save the request" not "normalize ran".
func tkRecordAnthropicNormalizeOpsEvent(c *gin.Context, changes []tkAnthropicNormalizeChange) {
	if c == nil || len(changes) == 0 {
		return
	}
	parts := make([]string, 0, len(changes))
	for _, ch := range changes {
		parts = append(parts, string(ch))
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: string(PlatformAnthropic),
		Kind:     "request_normalized",
		Message:  strings.Join(parts, ","),
	})
}

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
//  3. Claude Code prompt surfaces in system / system-reminder text: geo-stego
//     date lines, # Environment (TZ/proxy), and client userEmail. Rewritten
//     before US edge OAuth egress; userEmail is replaced with the scheduled
//     account OAuth email when available. See gateway_request_tk_cc_prompt_surface.go.
//
// Scope: Anthropic-platform Forward + count_tokens (OAuth path). API-key
// passthrough and Bedrock call tkApplyAnthropicCCPromptSurfaceNormalize at
// their entry points. The hook runs once
// per request before any other body rewrite so downstream code sees a
// well-formed body.
//
// Original client body is preserved on the gin context (handler.setOpsRequestModelAndBody
// via OpsRequestBodyKey) BEFORE this hook runs, so ops enrichment and RCA can still
// read the pre-normalize payload when normalize fires.
//
// Observability:
//   - Every successful normalization emits an INFO log
//     "gateway.anthropic_request_normalized" with request_id + the list of
//     changes applied. Use this to count total normalize hits.
//   - Post-normalize prompt fingerprint (classes + surface_signature, no full
//     system text) emits "gateway.anthropic_prompt_fingerprint" when normalize
//     fired, unknown surfaces detected, or ~1% sampled. See
//     gateway_request_tk_prompt_fingerprint.go + prompt_surface_registry.json.
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
func (s *GatewayService) tkNormalizeAnthropicRequestBody(ctx context.Context, c *gin.Context, body []byte, account *Account) []byte {
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

	oauthEmail := ""
	if account != nil {
		oauthEmail = account.GetOAuthAccountEmail()
	}
	beforePrompt := next
	if patched, applied := tkNormalizeAnthropicCCPromptSurface(next, oauthEmail); applied {
		next = patched
		changes = append(changes, tkAnthropicCCPromptSurfaceChanges(beforePrompt, next)...)
	}

	if len(changes) > 0 {
		tkLogAnthropicNormalize(ctx, changes)
		tkRecordAnthropicNormalizeOpsEvent(c, changes)
	}

	s.tkMaybeLogAnthropicPromptFingerprint(ctx, c, next, changes)
	return next
}

// tkApplyAnthropicCCPromptSurfaceNormalize runs prompt-surface normalize for
// paths that skip tkNormalizeAnthropicRequestBody (API-key passthrough, Bedrock).
func tkApplyAnthropicCCPromptSurfaceNormalize(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) ([]byte, []tkAnthropicNormalizeChange) {
	if len(body) == 0 {
		return body, nil
	}
	oauthEmail := ""
	if account != nil {
		oauthEmail = account.GetOAuthAccountEmail()
	}
	before := body
	out, applied := tkNormalizeAnthropicCCPromptSurface(body, oauthEmail)
	if !applied {
		return body, nil
	}
	changes := tkAnthropicCCPromptSurfaceChanges(before, out)
	if len(changes) > 0 {
		tkLogAnthropicNormalize(ctx, changes)
		tkRecordAnthropicNormalizeOpsEvent(c, changes)
	}
	return out, changes
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

// tkRecordAnthropicNormalizeOpsEvent attaches normalize change kinds to the
// request's upstream-errors event list so failed (>=400) rows retain them in
// upstream_errors JSON. Recovered-200 ops logging intentionally skips this kind;
// gateway.anthropic_request_normalized slog is the success-path audit trail.
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

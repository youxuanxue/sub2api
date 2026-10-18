package service

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicpolicy"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// tkAnthropicNormalizeChange identifies one normalization rule that was
// applied to a request body.
type tkAnthropicNormalizeChange string

const (
	tkNormalizeChangeToolChoiceStringToObject tkAnthropicNormalizeChange = "tool_choice_string_to_object"
	tkNormalizeChangeThinkingForcesToolUse    tkAnthropicNormalizeChange = anthropicpolicy.ThinkingPreferred
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

	if _, planned := ProtocolExecutionPlan(ctx); !planned {
		if patched, applied := tkNormalizeAnthropicThinkingForcesToolUse(next); applied {
			next = patched
			changes = append(changes, tkNormalizeChangeThinkingForcesToolUse)
		}
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

// Legacy non-Plan execution shares the thinking-first compatibility owner.
func tkNormalizeAnthropicThinkingForcesToolUse(body []byte) ([]byte, bool) {
	return anthropicpolicy.Normalize(body, true, anthropicpolicy.MessagesCapabilities(gjson.GetBytes(body, "model").String()))
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

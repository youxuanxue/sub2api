package service

// TK: See upstream Wei-Shaw/sub2api#2556.
//
// Silent-refusal detection for the OpenAI Chat Completions raw passthrough.
//
// Background:
//   Some OpenAI-compatible upstreams (observed on gpt-5.5 above an opaque
//   input-size threshold) "succeed" with a ghost stream — a role-only delta,
//   an empty content delta whose finish_reason is "stop", then [DONE] — and
//   omit the trailing `usage` chunk. forwardAsRawChatCompletions previously
//   treated this as a normal 200 response: zero usage was billed, no error
//   surfaced to the client, no ops_error_logs row was written, and the
//   account never failed over. This file detects that shape after the stream
//   (or buffered response) is observed, so the gateway emits a categorical
//   ops_error event ("silent_refusal") that runbooks and dashboards can
//   alert on.
//
// Detection is intentionally conservative: it must be a stop-class finish
// with zero observable output AND zero token accounting. Any tool call,
// any non-empty content delta, any reasoning_content, any refusal, or any
// non-zero token count disqualifies the stream from being flagged.
//
// Mitigation:
//   Stream path — HTTP headers are already flushed, so the bytes have left
//   the door. We do not rewrite the client-visible body. Instead we attach
//   the categorical ops event so the next layer (ops_error_logs, alerts,
//   account quality metrics) can react. The actual client experience for
//   the current request is unchanged; future requests benefit from the
//   account-level signal.
//
//   Buffer path — headers are not yet flushed in bufferRawChatCompletions
//   when the body is fully read, so a stricter mitigation (502 + failover)
//   would be possible. We deliberately stop at the ops event here too, to
//   keep behavior changes minimal and reviewable. Promotion to a real
//   failover is a follow-up.

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// silentRefusalKind is the OpsUpstreamErrorEvent.Kind label emitted when a
// ghost stream is detected. Kept as a constant so the registry / SQL audit
// in upstream Wei-Shaw/sub2api#2556 can grep for one canonical token.
const silentRefusalKind = "silent_refusal"

// silentRefusalMessage is the human-facing message stored on the ops event.
// Stable wording so log aggregation can pivot on it.
const silentRefusalMessage = "upstream returned an empty stream with finish_reason=stop and no usage; treated as a silent refusal"

// chatRawStreamObservations accumulates the signals needed to decide
// whether a raw chat-completions stream was a silent refusal. The zero
// value is a valid empty observation set.
type chatRawStreamObservations struct {
	sawContent       bool
	sawToolCalls     bool
	sawReasoning     bool
	sawRefusal       bool
	sawErrorEvent    bool
	lastFinishReason string
}

// Observe parses one SSE data payload (already stripped of the `data: `
// prefix and the `[DONE]` sentinel) and updates the observation set.
// Malformed JSON is ignored — silent-refusal detection should never throw.
func (o *chatRawStreamObservations) Observe(payload string) {
	if o == nil {
		return
	}
	if strings.TrimSpace(payload) == "" {
		return
	}
	if !gjson.Valid(payload) {
		return
	}
	// Top-level `error` is how some OpenAI-compatible upstreams flag a
	// chunk-level error mid-stream. If present, refuse to flag the stream
	// as a silent refusal — the upstream did surface something.
	if gjson.Get(payload, "error").Exists() {
		o.sawErrorEvent = true
	}
	gjson.Get(payload, "choices").ForEach(func(_, choice gjson.Result) bool {
		if v := choice.Get("delta.content"); v.Exists() && v.Type == gjson.String && v.String() != "" {
			o.sawContent = true
		}
		if v := choice.Get("delta.reasoning_content"); v.Exists() && v.Type == gjson.String && v.String() != "" {
			o.sawReasoning = true
		}
		if v := choice.Get("delta.refusal"); v.Exists() && v.Type == gjson.String && v.String() != "" {
			o.sawRefusal = true
		}
		if tc := choice.Get("delta.tool_calls"); tc.Exists() && tc.IsArray() && len(tc.Array()) > 0 {
			o.sawToolCalls = true
		}
		if fr := choice.Get("finish_reason"); fr.Exists() && fr.Type == gjson.String && fr.String() != "" {
			o.lastFinishReason = fr.String()
		}
		return true
	})
}

// ObserveBufferedResponse parses a non-streaming Chat Completions response
// body and updates the observation set. Used by the buffered path so the
// same predicate can decide silent refusal in both flows.
func (o *chatRawStreamObservations) ObserveBufferedResponse(body []byte) {
	if o == nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return
	}
	if gjson.GetBytes(body, "error").Exists() {
		o.sawErrorEvent = true
	}
	gjson.GetBytes(body, "choices").ForEach(func(_, choice gjson.Result) bool {
		if v := choice.Get("message.content"); v.Exists() && v.Type == gjson.String && v.String() != "" {
			o.sawContent = true
		}
		if v := choice.Get("message.reasoning_content"); v.Exists() && v.Type == gjson.String && v.String() != "" {
			o.sawReasoning = true
		}
		if v := choice.Get("message.refusal"); v.Exists() && v.Type == gjson.String && v.String() != "" {
			o.sawRefusal = true
		}
		if tc := choice.Get("message.tool_calls"); tc.Exists() && tc.IsArray() && len(tc.Array()) > 0 {
			o.sawToolCalls = true
		}
		if fr := choice.Get("finish_reason"); fr.Exists() && fr.Type == gjson.String && fr.String() != "" {
			o.lastFinishReason = fr.String()
		}
		return true
	})
}

// IsSilentRefusal reports whether the observations match the ghost-stream
// shape described in upstream Wei-Shaw/sub2api#2556.
//
// All of the following must hold:
//   - finish_reason == "stop" (length/content_filter/tool_calls/null exempt)
//   - no observed delta.content / message.content
//   - no observed delta.tool_calls / message.tool_calls
//   - no observed delta.reasoning_content / message.reasoning_content
//   - no observed delta.refusal / message.refusal
//   - no observed chunk-level `error` field
//   - usage.input_tokens == 0 AND usage.output_tokens == 0
//
// Requiring zero usage in addition to zero observable output is what makes
// the predicate safe against a legitimately short answer where the upstream
// happens not to send incremental content — that case still records non-zero
// prompt tokens. Silent refusals consistently arrive with zero/missing usage.
func (o *chatRawStreamObservations) IsSilentRefusal(usage OpenAIUsage) bool {
	if o == nil {
		return false
	}
	if o.sawContent || o.sawToolCalls || o.sawReasoning || o.sawRefusal || o.sawErrorEvent {
		return false
	}
	if o.lastFinishReason != "stop" {
		return false
	}
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		return false
	}
	return true
}

// recordOpenAIChatRawSilentRefusal attaches the silent-refusal signal to the
// request's ops context (for ops_error_logs aggregation) without modifying
// the client-visible response. Safe to call with a nil gin context (no-op).
func recordOpenAIChatRawSilentRefusal(c *gin.Context, account *Account, requestID string) {
	setOpsUpstreamError(c, 0, silentRefusalMessage, "")
	if account == nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			UpstreamRequestID: requestID,
			Kind:              silentRefusalKind,
			Message:           silentRefusalMessage,
		})
		return
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:          account.Platform,
		AccountID:         account.ID,
		AccountName:       account.Name,
		UpstreamRequestID: requestID,
		Kind:              silentRefusalKind,
		Message:           silentRefusalMessage,
	})
}

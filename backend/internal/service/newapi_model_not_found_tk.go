package service

import (
	"strings"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// IsOpenAICompatModelNotFound404 reports whether an OpenAI-compatible / newapi
// upstream 404 is a CALLER-fault "the requested model does not exist / is not
// accessible on this channel" rather than a provider-health failure. The newapi
// fifth-platform sibling of IsAnthropicModelNotFound404. Two real upstream shapes
// (both captured by direct probe, 2026-06-10):
//
//	VolcEngine Ark (channel_type=45): un-activated / retired model →
//	  {"error":{"code":"InvalidEndpointOrModel.NotFound",
//	            "message":"The model `X` does not exist or you do not have access to it."}}
//
//	DashScope / Qwen (channel_type=17, OpenAI-compatible mode): unknown / retired
//	  model → HTTP 404 with the OpenAI-standard envelope
//	  {"error":{"code":"model_not_found",
//	            "message":"The model `X` does not exist or you do not have access to it."}}
//
// (DeepSeek, channel_type=43, instead returns HTTP 400 invalid_request_error for an
// unknown model — "The supported API model names are ...". That is already owned to
// the client by the 400 invalid_request_error branch in tkUpstreamClientInducedRejection,
// so it never reaches this 404 helper.)
//
// Both the prose phrase ("does not exist or you do not have access") and the
// structured code ("model_not_found" / "InvalidEndpointOrModel.NotFound") are
// matched, so the classification survives a vendor changing one without the other.
//
// classifyOpsErrorLog uses this (via tkUpstreamClientInducedRejection) to own the
// error to the client (phase=request → error_owner=client), keeping it OUT of
// upstream_error_rate. Without it a single channel that advertises an un-activated
// model lets every client request inflate the provider-health P0 capacity alert —
// the 2026-06-10 false P0 (account 7 volcengine, ~130×502 in 12 min).
func IsOpenAICompatModelNotFound404(responseBody []byte, upstreamMsg string) bool {
	combined := strings.ToLower(strings.TrimSpace(upstreamMsg + "\n" + string(responseBody)))
	if combined == "" {
		return false
	}
	if strings.Contains(combined, "invalidendpointormodel.notfound") ||
		strings.Contains(combined, "model_not_found") ||
		strings.Contains(combined, "does not exist or you do not have access") {
		return true
	}
	if code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "error.code").String())); code == "invalidendpointormodel.notfound" || code == "model_not_found" {
		return true
	}
	return false
}

// TkRecordBridgeUpstreamError records the REAL upstream HTTP status + error
// message/code from a New API bridge relay error into the ops context, so ops
// error classification (classifyOpsErrorLog) sees the true upstream verdict.
//
// Why: newapi bridge forward failures frequently fall through to the generic
// "Upstream request failed" fallback (ensureForwardErrorResponse), which never
// calls SetOpsUpstreamError. ops_error_logs then records upstream_status_code=null
// and — because error_type=upstream_error alone classifies as phase=upstream —
// a caller-fault upstream 404 model-not-found is mis-owned as provider health and
// pollutes upstream_error_rate (the 2026-06-10 false P0). Recording the real 404
// at the bridge (the one layer where the upstream status is guaranteed available)
// lets tkUpstreamClientInducedRejection reclassify it phase=request, independent
// of which downstream response path runs.
//
// The error CODE is prefixed into the recorded message because the ops classifier
// reads only the single-field message key (tkOpsUpstreamErrorText), not the detail
// key, so the InvalidEndpointOrModel.NotFound signal must travel in the message.
func TkRecordBridgeUpstreamError(c *gin.Context, upstreamStatusCode int, err *newapitypes.NewAPIError) {
	if c == nil || err == nil {
		return
	}
	code := strings.TrimSpace(string(err.GetErrorCode()))
	msg := strings.TrimSpace(err.Error())
	if code != "" {
		msg = code + ": " + msg
	}
	SetOpsUpstreamError(c, upstreamStatusCode, msg, code)
}

// tkWrapBridgeRelayError records the real upstream status of a New API bridge
// dispatch error (see TkRecordBridgeUpstreamError) and wraps it as a
// *NewAPIRelayError. Use at every dispatch site that wraps a real bridge upstream
// error (NOT the synthetic missing-credential / unsupported-channel errors, which
// carry no upstream verdict). This is the TokenKey-service chokepoint between the
// bridge (which cannot call up into internal/service) and the handler.
func tkWrapBridgeRelayError(c *gin.Context, apiErr *newapitypes.NewAPIError) *NewAPIRelayError {
	if apiErr != nil {
		TkRecordBridgeUpstreamError(c, apiErr.StatusCode, apiErr)
	}
	return &NewAPIRelayError{Err: apiErr}
}

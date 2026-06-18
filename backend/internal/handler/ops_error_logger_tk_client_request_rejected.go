package handler

import "github.com/gin-gonic/gin"

// opsClientRequestRejectedKey marks a request the GATEWAY rejected LOCALLY as a
// client request error — no account was selected and no upstream call was made.
// Today the sole setter is the unservable-model fast-fail (service.ErrUnsupportedModel
// → 400 invalid_request_error) emitted by tkSelectFailureStatusMessage and
// tkWriteUnsupportedModelIfApplicable.
//
// Why a context flag instead of letting classifyOpsErrorLog read the response body:
// the OpenAI-compat error writers emit DIFFERENT envelopes for the SAME logical
// error. chatCompletionsErrorResponse writes {"error":{"type":"invalid_request_error"}}
// (the ops parser reads `type` → phase=request → owner=client), but the native
// responsesErrorResponse writes the OpenAI Responses shape
// {"error":{"code":"invalid_request_error"}} with NO top-level `type`, so
// parseOpsErrorResponse flattens it to ErrorType="api_error" →
// classifyOpsPhase→"internal" → owner=platform. That mislabels a client fault as a
// TK gateway error and pollutes the gateway error rate — the inverse of the
// empty-pool 429's routing-capacity mislabel this whole change set is removing.
//
// The flag makes the attribution depend on what the gateway KNOWS (this is a client
// request rejection) rather than on response-envelope archaeology, so it is correct
// for every endpoint regardless of envelope. It mirrors the upstream-variant
// tkUpstreamClientInducedRejection (which owns an upstream-returned client 4xx to
// the client); this is its local, pre-forward counterpart.
const opsClientRequestRejectedKey = "ops_client_request_rejected"

// markOpsClientRequestRejected records that the gateway locally rejected this
// request as a client error. Safe on a nil context.
func markOpsClientRequestRejected(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(opsClientRequestRejectedKey, true)
}

// hasOpsClientRequestRejected reports whether markOpsClientRequestRejected ran for
// this request.
func hasOpsClientRequestRejected(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(opsClientRequestRejectedKey)
	if !ok {
		return false
	}
	marked, _ := v.(bool)
	return marked
}

package handler

import (
	"github.com/gin-gonic/gin"
)

// setOpsRequestModelAndBody is a TK companion of setOpsRequestContext.
//
// Upstream Wei-Shaw/sub2api commit 2eb622f2 ("Remove ops retry replay
// storage") dropped the `body []byte` parameter from setOpsRequestContext
// along with the entire ops_retry_replay infra. TokenKey still wants the
// raw request body in the gin context so service.TkEnrichForbiddenMessage
// (gateway_service_tk_upstream_error_msg.go) can rewrite the upstream-403
// default message into a body-size-aware hint, including the byte count.
//
// This helper bundles the upstream-shaped `setOpsRequestContext(c, model,
// stream)` call with a `c.Set(opsRequestBodyKey, body)` push, so handler
// entry points can opt-in to body-aware enrichment with one call instead
// of sprinkling the `c.Set` line at every site (and risking it drifting
// from setOpsRequestContext at the next upstream merge).
//
// Wire contract: `opsRequestBodyKey == service.OpsRequestBodyKey` — see
// ops_error_logger.go const block and ops_upstream_context.go for the
// service-side reader. The corresponding test that proves the cross-
// package contract still holds is TestOpsKeyContract_HandlerWriteServiceRead
// in ops_error_logger_test.go.
func setOpsRequestModelAndBody(c *gin.Context, model string, stream bool, body []byte) {
	setOpsRequestContext(c, model, stream)
	if c == nil || len(body) == 0 {
		return
	}
	c.Set(opsRequestBodyKey, body)
}

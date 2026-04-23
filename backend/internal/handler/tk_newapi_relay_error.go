package handler

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// TkTryWriteNewAPIRelayErrorJSON maps err to NewAPIRelayError and writes an OpenAI-shaped JSON error
// when no streamed bytes were written. Returns true when err was a relay error (caller should stop).
//
// Service-layer responsibility split for newapi bridge errors:
//   - openai_gateway_bridge_dispatch.go (chat / responses / embeddings / images)
//     and gateway_bridge_dispatch.go: do NOT write the response body. Handler
//     calls TkTryWriteNewAPIRelayErrorJSON which serialises in OpenAI shape.
//   - openai_gateway_bridge_dispatch_tk_anthropic.go (Anthropic-via-chat):
//     DOES write a ClaudeError body before returning *NewAPIRelayError, so
//     the Anthropic Messages handler MUST NOT then call this helper (it
//     would emit a second OpenAI-shape body on top, corrupting the response).
//
// Bug B-2: the asymmetry above is a soft contract — easy to break by
// reflexively wiring TkTryWriteNewAPIRelayErrorJSON into the Anthropic
// Messages handler "for consistency". The double-write guard below converts
// the contract to a mechanical check: if ANY response bytes were written
// since writerSizeBeforeForward (or stream started), skip the JSON write
// and just report `true` so the caller stops. The guard is OPC-philosophy:
// turn a convention into a runtime check that fails loud rather than silent.
//
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-2 / § B-11.
func TkTryWriteNewAPIRelayErrorJSON(c *gin.Context, err error, streamStarted bool, writerSizeBeforeForward int) bool {
	var nre *service.NewAPIRelayError
	if !errors.As(err, &nre) || nre == nil || nre.Err == nil {
		return false
	}
	// Bug B-2: double-write guard — if the service layer (Anthropic bridge
	// dispatch) already wrote a response body before returning the
	// NewAPIRelayError, do NOT write a second OpenAI-shape body on top.
	// We still return true so the caller stops further forwarding logic.
	if streamStarted || c.Writer.Size() != writerSizeBeforeForward || c.Writer.Written() {
		return true
	}
	// Bug B-11: bridge errors built via certain new-api ErrOption combos can
	// carry StatusCode == 0 (or other non-HTTP-error values), which would
	// emit `c.JSON(0, ...)` → an actual HTTP 200 response with a JSON error
	// body. Clients then mis-interpret a failure as success. Coerce any
	// out-of-range status to 502 Bad Gateway, mirroring gateway forward
	// fallbacks. See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-11.
	status := nre.Err.StatusCode
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	c.JSON(status, gin.H{"error": nre.Err.ToOpenAIError()})
	return true
}

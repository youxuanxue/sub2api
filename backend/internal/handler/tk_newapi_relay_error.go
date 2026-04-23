package handler

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// TkTryWriteNewAPIRelayErrorJSON maps err to NewAPIRelayError and writes an OpenAI-shaped JSON error
// when no streamed bytes were written. Returns true when err was a relay error (caller should stop).
func TkTryWriteNewAPIRelayErrorJSON(c *gin.Context, err error, streamStarted bool, writerSizeBeforeForward int) bool {
	var nre *service.NewAPIRelayError
	if !errors.As(err, &nre) || nre == nil || nre.Err == nil {
		return false
	}
	if c.Writer.Size() == writerSizeBeforeForward && !streamStarted {
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
	}
	return true
}

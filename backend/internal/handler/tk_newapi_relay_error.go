package handler

import (
	"errors"

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
		c.JSON(nre.Err.StatusCode, gin.H{"error": nre.Err.ToOpenAIError()})
	}
	return true
}

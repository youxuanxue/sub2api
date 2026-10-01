package handler

import (
	"errors"
	"fmt"
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
		c.JSON(nre.Err.StatusCode, gin.H{"error": nre.Err.ToOpenAIError()})
	}
	return true
}

func tkTryWriteResponsesRelayClientError(c *gin.Context, err error, streamStarted bool) bool {
	if !inboundIsResponses(c) {
		return false
	}
	var relayErr *service.NewAPIRelayError
	if !errors.As(err, &relayErr) {
		return false
	}
	payload, ok := relayErr.ResponsesClientError()
	if !ok {
		return false
	}
	if streamStarted {
		code := ""
		if payload.Code != nil {
			code = fmt.Sprint(payload.Code)
		}
		service.MarkOpsStreamError(c, payload.Type, payload.Message, http.StatusBadRequest)
		writeResponsesFailedSSE(c, payload.Type, code, payload.Message)
		return true
	}
	c.Header("Content-Type", "application/json")
	c.JSON(http.StatusBadRequest, gin.H{"error": payload})
	return true
}

package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type gatewayErrorResponseFunc func(*gin.Context, int, string, string)

func isClientClosedRequest(c *gin.Context, err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if c == nil || c.Request == nil {
		return false
	}
	return errors.Is(c.Request.Context().Err(), context.Canceled)
}

func writeClientClosedRequest(c *gin.Context, write gatewayErrorResponseFunc) {
	service.MarkOpsClientClosedRequest(c)
	write(c, statusClientClosedRequest, "api_error", "context canceled")
}

// markClientClosedForwardRequest records a Forward-time caller cancellation
// without writing a body to a connection that no longer has a receiver.
func markClientClosedForwardRequest(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	service.MarkOpsClientClosedRequest(c)
	// Stop compact keepalive before touching the writer. A committed keepalive
	// has already fixed the HTTP status at 200, so only the ops classification
	// can still be corrected.
	if service.StopOpenAICompactSSEKeepaliveCommitted(c) {
		return
	}
	if !c.Writer.Written() {
		c.Status(statusClientClosedRequest)
	}
}

func writeReadRequestBodyError(c *gin.Context, err error, write gatewayErrorResponseFunc) {
	if maxErr, ok := extractMaxBytesError(err); ok {
		write(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
		return
	}
	if isClientClosedRequest(c, err) {
		writeClientClosedRequest(c, write)
		return
	}
	write(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
}

func writeGoogleReadRequestBodyError(c *gin.Context, err error) {
	writeReadRequestBodyError(c, err, func(c *gin.Context, status int, _ string, message string) {
		googleError(c, status, message)
	})
}

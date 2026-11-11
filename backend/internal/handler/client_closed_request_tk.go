package handler

import (
	"errors"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type gatewayErrorResponseFunc func(*gin.Context, int, string, string)

// isClientClosedRequest delegates to the service-layer owner
// (client-closed-499-ssot; service/client_closed_request_tk.go) so body-read
// and forward-time cancellation share the same precedence (deadline
// exclusion, wrapped Canceled, request context, lib/pq 57014 text) as the
// auth middlewares.
func isClientClosedRequest(c *gin.Context, err error) bool {
	return service.IsClientClosedRequest(c, err)
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
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		write(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
		return
	}
	// net/http also cancels HTTP/1 request contexts on read timeouts.
	// Preserve the typed 408 classification before checking caller cancellation.
	if pkghttputil.DescribeRequestBodyReadFailure(nil, err).Status != http.StatusRequestTimeout && isClientClosedRequest(c, err) {
		writeClientClosedRequest(c, write)
		return
	}
	var req *http.Request
	if c != nil {
		req = c.Request
	}
	status, message := requestBodyReadFailure(logger.L(), req, err)
	write(c, status, "invalid_request_error", message)
}

func writeGoogleReadRequestBodyError(c *gin.Context, err error) {
	writeReadRequestBodyError(c, err, func(c *gin.Context, status int, _ string, message string) {
		googleError(c, status, message)
	})
}

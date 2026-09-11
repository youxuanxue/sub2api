package middleware

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const clientRequestIDHeader = "X-Client-Request-ID"

// ClientRequestID ensures every gateway request has a client_request_id.
//
// Priority: existing context → inbound X-Client-Request-ID → inbound X-Request-ID
// (compat only) → generated UUID. The X-Request-ID fallback exists so callers that
// still send only the legacy header keep a searchable marker when the response
// never returns; it never becomes ctxkey.RequestID / QA / usage storage identity.
func ClientRequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		id := ""
		if v, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string); strings.TrimSpace(v) != "" {
			if normalized, ok := normalizeCorrelationID(v); ok {
				id = normalized
			}
		}
		if id == "" {
			if normalized, ok := normalizeCorrelationID(c.GetHeader(clientRequestIDHeader)); ok {
				id = normalized
			}
		}
		if id == "" {
			// Legacy clients often only send X-Request-ID. Keep that value as the
			// client marker so a dropped response is still correlatable in logs/ops,
			// while RequestLogger continues to mint a distinct server request_id.
			if normalized, ok := normalizeCorrelationID(c.GetHeader(requestIDHeader)); ok {
				id = normalized
			}
		}
		if id == "" {
			id = uuid.New().String()
		}

		c.Header(clientRequestIDHeader, id)
		ctx := context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, id)
		requestLogger := logger.FromContext(ctx).With(zap.String("client_request_id", strings.TrimSpace(id)))
		ctx = logger.IntoContext(ctx, requestLogger)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// TrajectoryID ensures every request has a stable trajectory_id in request.Context().
func TrajectoryID() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		if v := c.Request.Context().Value(ctxkey.TrajectoryID); v != nil {
			c.Next()
			return
		}

		id := uuid.New().String()
		ctx := context.WithValue(c.Request.Context(), ctxkey.TrajectoryID, id)
		requestLogger := logger.FromContext(ctx).With(zap.String("trajectory_id", strings.TrimSpace(id)))
		ctx = logger.IntoContext(ctx, requestLogger)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

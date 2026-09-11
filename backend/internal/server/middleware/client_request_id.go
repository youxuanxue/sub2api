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
// Priority: existing context value → inbound X-Client-Request-ID → generated UUID.
// This is the client-chosen correlation marker for ops/billing evidence. It is
// never the storage identity: X-Request-ID / ctxkey.RequestID stay server-only.
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

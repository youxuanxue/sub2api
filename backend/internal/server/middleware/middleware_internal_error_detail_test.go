package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSanitizeMiddlewareInternalErrorDetail_TrimsAndCaps(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		require.Equal(t, "", sanitizeMiddlewareInternalErrorDetail(nil))
	})
	t.Run("empty after trim", func(t *testing.T) {
		require.Equal(t, "", sanitizeMiddlewareInternalErrorDetail(errors.New("   ")))
	})
	t.Run("short message preserved", func(t *testing.T) {
		require.Equal(t, "redis ECONNREFUSED", sanitizeMiddlewareInternalErrorDetail(errors.New("  redis ECONNREFUSED  ")))
	})
	t.Run("long message truncated", func(t *testing.T) {
		long := strings.Repeat("x", middlewareInternalErrorDetailMaxLen+128)
		got := sanitizeMiddlewareInternalErrorDetail(errors.New(long))
		require.True(t, strings.HasSuffix(got, "...(truncated)"))
		require.True(t, len(got) <= middlewareInternalErrorDetailMaxLen+len("...(truncated)"))
	})
}

func TestAbortWithErrorDetail_SetsOpsKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	AbortWithErrorDetail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to validate API key", errors.New("redis ECONNREFUSED 127.0.0.1:6379"))

	v, ok := c.Get(service.OpsInternalErrorDetailKey)
	require.True(t, ok)
	s, ok := v.(string)
	require.True(t, ok)
	require.Equal(t, "redis ECONNREFUSED 127.0.0.1:6379", s)
}

func TestAbortWithErrorDetail_NilErrorDoesNotSetKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	AbortWithErrorDetail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to validate API key", nil)

	_, ok := c.Get(service.OpsInternalErrorDetailKey)
	require.False(t, ok)
}

func TestAbortClientClosedRequest_SetsOpsMarkerAndDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	AbortClientClosedRequest(c, context.Canceled)

	require.Equal(t, StatusClientClosedRequest, rec.Code)
	require.True(t, service.HasOpsClientClosedRequest(c))
	v, ok := c.Get(service.OpsInternalErrorDetailKey)
	require.True(t, ok)
	require.Equal(t, "context canceled", v)
	require.Contains(t, rec.Body.String(), "CLIENT_CLOSED_REQUEST")
}

func TestAPIKeyAuth_500_SetsOpsInternalErrorDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKeyService := newTestAPIKeyService(fakeAPIKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			return nil, errors.New("postgres conn pool exhausted")
		},
	})

	r := gin.New()
	var captured *gin.Context
	r.Use(func(c *gin.Context) { captured = c; c.Next() })
	r.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, &config.Config{})))
	r.POST("/v1/messages", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer any")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotNil(t, captured)
	v, ok := captured.Get(service.OpsInternalErrorDetailKey)
	require.True(t, ok, "ops internal-error detail key should be set on 500")
	s, ok := v.(string)
	require.True(t, ok)
	require.Contains(t, s, "postgres conn pool exhausted")
	// Response body must not leak detail — client still sees the generic message.
	require.NotContains(t, rec.Body.String(), "postgres conn pool exhausted")
}

func TestAPIKeyAuth_ContextCanceled_ReturnsClientClosedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKeyService := newTestAPIKeyService(fakeAPIKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			return nil, context.Canceled
		},
	})

	r := gin.New()
	var captured *gin.Context
	r.Use(func(c *gin.Context) { captured = c; c.Next() })
	r.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, &config.Config{})))
	r.POST("/v1/messages", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer any")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, StatusClientClosedRequest, rec.Code)
	require.NotNil(t, captured)
	require.True(t, service.HasOpsClientClosedRequest(captured))
	require.Contains(t, rec.Body.String(), "context canceled")
	require.NotContains(t, rec.Body.String(), "Failed to validate API key")
}

func TestAPIKeyAuthGoogle_500_SetsOpsInternalErrorDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKeyService := newTestAPIKeyService(fakeAPIKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			return nil, errors.New("context deadline exceeded")
		},
	})

	r := gin.New()
	var captured *gin.Context
	r.Use(func(c *gin.Context) { captured = c; c.Next() })
	r.Use(APIKeyAuthWithSubscriptionGoogle(apiKeyService, nil, &config.Config{}))
	r.GET("/v1beta/test", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodGet, "/v1beta/test", nil)
	req.Header.Set("Authorization", "Bearer any")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotNil(t, captured)
	v, ok := captured.Get(service.OpsInternalErrorDetailKey)
	require.True(t, ok, "ops internal-error detail key should be set on google 500")
	s, ok := v.(string)
	require.True(t, ok)
	require.Contains(t, s, "context deadline exceeded")
	require.NotContains(t, rec.Body.String(), "context deadline exceeded")
}

func TestAPIKeyAuthGoogle_ContextCanceled_ReturnsClientClosedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKeyService := newTestAPIKeyService(fakeAPIKeyRepo{
		getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
			return nil, context.Canceled
		},
	})

	r := gin.New()
	var captured *gin.Context
	r.Use(func(c *gin.Context) { captured = c; c.Next() })
	r.Use(APIKeyAuthWithSubscriptionGoogle(apiKeyService, nil, &config.Config{}))
	r.GET("/v1beta/test", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	req := httptest.NewRequest(http.MethodGet, "/v1beta/test", nil)
	req.Header.Set("Authorization", "Bearer any")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, StatusClientClosedRequest, rec.Code)
	require.NotNil(t, captured)
	require.True(t, service.HasOpsClientClosedRequest(captured))
	require.Contains(t, rec.Body.String(), "context canceled")
	require.Contains(t, rec.Body.String(), "CANCELLED")
	require.NotContains(t, rec.Body.String(), "Failed to validate API key")
}

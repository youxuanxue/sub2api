//go:build unit

package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageIngressAccessLogPreservesBodyAndCountsReplayOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLoggerWithLevel(t, "info")
	body := `{"model":"gpt-image-2","prompt":"private test prompt"}`
	router := gin.New()
	router.Use(RequestLogger(), Logger())
	router.POST("/v1/images/generations", func(c *gin.Context) {
		first, err := readAndRestoreUniversalBody(c)
		require.NoError(t, err)
		second, err := readAndRestoreUniversalBody(c)
		require.NoError(t, err)
		require.Equal(t, body, string(first))
		require.Equal(t, first, second)
		// Even the direct/no-resolver return must close the entry span.
		require.False(t, MaybeResolveUniversal(c, nil, nil))
		replay, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, body, string(replay))
		c.String(http.StatusOK, "ok")
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "ok", w.Body.String())
	for _, event := range sink.list() {
		if event.Message != "http request completed" {
			continue
		}
		require.Equal(t, int64(len(body)), event.Fields["image_request_body_bytes"])
		require.Equal(t, int64(len(body)), event.Fields["image_request_content_length"])
		require.LessOrEqual(t, event.Fields["image_ingress_ms"].(int64), event.Fields["latency_ms"].(int64))
		require.NotContains(t, event.Fields, "image_ingress_candidates_ms")
		require.NotContains(t, event.Fields, "prompt")
		require.NotContains(t, event.Fields, "authorization")
		return
	}
	t.Fatal("HTTP completion log not emitted")
}

func TestImageIngressRejectedAuthDoesNotReadBodyOrRunRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLoggerWithLevel(t, "info")
	api := newTestAPIKeyService(fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		t.Fatal("missing key must not reach repository")
		return nil, nil
	}})
	router := gin.New()
	router.Use(Logger(), gin.HandlerFunc(NewAPIKeyAuthMiddleware(api, nil, nil, &config.Config{})))
	router.POST("/v1/images/edits", func(*gin.Context) { t.Fatal("rejected auth reached handler") })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader("private upload")))
	require.Equal(t, http.StatusUnauthorized, w.Code)
	for _, event := range sink.list() {
		if event.Message != "http request completed" {
			continue
		}
		require.Equal(t, int64(0), event.Fields["image_request_body_bytes"])
		require.Equal(t, int64(0), event.Fields["image_request_body_read_ms"])
		require.GreaterOrEqual(t, event.Fields["image_ingress_key_auth_ms"].(int64), int64(0))
		require.NotContains(t, event.Fields, "image_ingress_ms")
		require.NotContains(t, event.Fields, "image_ingress_candidates_ms")
		return
	}
	t.Fatal("HTTP completion log not emitted")
}

func TestImageIngressAuthRoutingAndLoggingKeepSelectionBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLoggerWithLevel(t, "info")
	api, _, key, accounts := newCandidateHTTPServices(t, true)
	key.User.TotalRecharged = 10
	for i := range accounts.accounts {
		accounts.accounts[i].Platform = service.PlatformOpenAI
		accounts.accounts[i].Credentials["model_mapping"] = map[string]string{service.DefaultOpenAIImagesModel: service.DefaultOpenAIImagesModel}
	}
	body := `{"prompt":"private test prompt"}`
	router := gin.New()
	router.Use(RequestLogger(), Logger(), gin.HandlerFunc(NewAPIKeyAuthMiddleware(api, nil, nil, &config.Config{RunMode: config.RunModeStandard})))
	router.POST("/v1/images/generations", func(c *gin.Context) {
		require.Equal(t, service.DefaultOpenAIImagesModel, service.CandidateEffectiveModel(c.Request.Context(), ""))
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, body, string(raw))
		c.String(http.StatusOK, "ok")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer candidate-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	for _, event := range sink.list() {
		if event.Message != "http request completed" {
			continue
		}
		require.Equal(t, int64(len(body)), event.Fields["image_request_body_bytes"])
		ingress := event.Fields["image_ingress_ms"].(int64)
		selection := event.Fields["image_ingress_selection_ms"].(int64)
		require.LessOrEqual(t, selection, ingress)
		require.LessOrEqual(t, event.Fields["image_ingress_key_auth_ms"].(int64), ingress)
		require.LessOrEqual(t, event.Fields["image_ingress_candidates_ms"].(int64), selection)
		require.LessOrEqual(t, event.Fields["image_ingress_bind_ms"].(int64), selection)
		return
	}
	t.Fatal("HTTP completion log not emitted")
}

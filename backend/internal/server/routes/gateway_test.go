package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newGatewayRoutesTestRouter(platform string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	groupID := int64(1)

	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
				ID:      1,
				GroupID: &groupID,
				Group:   &service.Group{ID: groupID, Platform: platform},
			})
			c.Next()
		}),
		nil,
		nil,
		nil,
		nil,
		&config.Config{},
	)

	return router
}

func TestGatewayRoutesOpenAIResponsesCompactPathIsRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformOpenAI)

	for _, path := range []string{
		"/v1/responses/compact",
		"/responses/compact",
		"/backend-api/codex/responses",
		"/backend-api/codex/responses/compact",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit OpenAI responses handler", path)
	}
}

func TestGatewayRoutesNewAPICompatPathsAreRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformNewAPI)

	for _, path := range []string{
		"/v1/messages",
		"/v1/responses",
		"/v1/chat/completions",
		"/v1/embeddings",
		"/v1/images/generations",
		"/responses",
		"/chat/completions",
		"/embeddings",
		"/images/generations",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should be routed for newapi/openai-compatible groups", path)
	}
}

func TestGatewayRoutesOpenAIImagesPathsAreRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformOpenAI)

	for _, path := range []string{
		"/v1/images/generations",
		"/v1/images/edits",
		"/images/generations",
		"/images/edits",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-image-2","prompt":"draw a cat"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit OpenAI images handler", path)
	}
}

// TestGatewayRoutesVideoGenerationPathsAreRegistered protects the four async
// video task routes added for the fifth platform `newapi` (volcengine /
// doubaovideo). The async task registry is required for the actual handler
// to do work, but this test only asserts the route table is wired (i.e. the
// router does NOT return 404). A regression that drops any of these four
// paths would silently disable all volcengine video generation.
func TestGatewayRoutesVideoGenerationPathsAreRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformNewAPI)

	postPaths := []string{
		"/v1/video/generations",
		"/v1/videos",
		"/video/generations",
		"/videos",
	}
	for _, path := range postPaths {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"doubao-seedance","prompt":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "POST path=%s should be routed for newapi/openai-compatible groups", path)
	}

	getPaths := []string{
		"/v1/video/generations/abc",
		"/v1/videos/abc",
		"/video/generations/abc",
		"/videos/abc",
	}
	for _, path := range getPaths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "GET path=%s should be routed for newapi/openai-compatible groups", path)
	}
}

// TestGatewayRoutesVideoGenerationRejectsNonCompatPlatform proves the
// platform gating in tkOpenAICompatVideoSubmitHandler / VideoFetchHandler
// returns 404 for groups whose platform is NOT in OpenAICompatPlatforms()
// (e.g. anthropic). This is the inverse safety check — without it an
// anthropic group would route to OpenAIGateway.VideoSubmit which would
// crash on a nil group platform during account selection.
func TestGatewayRoutesVideoGenerationRejectsNonCompatPlatform(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformAnthropic)

	for _, path := range []string{
		"/v1/video/generations",
		"/v1/videos",
		"/video/generations",
		"/videos",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"any","prompt":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "POST path=%s on anthropic group should 404", path)
	}
}

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

	for _, path := range []string{"/v1/responses/compact", "/responses/compact"} {
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

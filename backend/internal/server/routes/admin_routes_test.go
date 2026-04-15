package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newAdminRoutesTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")

	RegisterAdminRoutes(
		v1,
		&handler.Handlers{
			Admin: &handler.AdminHandlers{
				Channel:   &adminhandler.ChannelHandler{},
				TKChannel: &adminhandler.TKChannelAdminHandler{},
			},
		},
		servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
			c.Next()
		}),
	)

	return router
}

func TestAdminRoutesTokenKeyChannelHelpersAreRegistered(t *testing.T) {
	router := newAdminRoutesTestRouter()

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/admin/channel-types", ""},
		{http.MethodGet, "/api/v1/admin/channel-type-models", ""},
		{http.MethodPost, "/api/v1/admin/channel-types/fetch-upstream-models", `{}`},
		{http.MethodPost, "/api/v1/admin/channels/aggregated-group-models", `{}`},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
		if tt.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should be registered", tt.path)
	}
}

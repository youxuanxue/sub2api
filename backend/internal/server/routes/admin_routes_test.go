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
				Channel:        &adminhandler.ChannelHandler{},
				TKChannel:      &adminhandler.TKChannelAdminHandler{},
				SupplierSource: &adminhandler.SupplierSourceHandler{},
			},
		},
		servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
			c.Next()
		}),
		servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }),
		servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() }),
		nil,
		nil,
	)

	return router
}

func TestUS048_SupplierSourceRoutesAreRegistered(t *testing.T) {
	router := newAdminRoutesTestRouter()
	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, tt := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/admin/supplier-sources"},
		{http.MethodPost, "/api/v1/admin/supplier-sources"},
		{http.MethodGet, "/api/v1/admin/supplier-sources/priority-preview"},
		{http.MethodGet, "/api/v1/admin/supplier-sources/:id"},
		{http.MethodPut, "/api/v1/admin/supplier-sources/:id"},
		{http.MethodPost, "/api/v1/admin/supplier-sources/:id/discover"},
		{http.MethodGet, "/api/v1/admin/supplier-sources/:id/discover/jobs/:job_id"},
		{http.MethodPost, "/api/v1/admin/supplier-sources/:id/probe"},
		{http.MethodGet, "/api/v1/admin/supplier-sources/:id/probe/jobs/:job_id"},
		{http.MethodPost, "/api/v1/admin/supplier-sources/:id/validate"},
		{http.MethodPost, "/api/v1/admin/supplier-sources/:id/sync"},
	} {
		require.True(t, registered[tt.method+" "+tt.path], "path=%s should be registered", tt.path)
	}

	for _, tt := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/admin/supplier-sources/:id/activation-preview"},
		{http.MethodGet, "/api/v1/admin/supplier-sources/:id/audits"},
		{http.MethodPost, "/api/v1/admin/supplier-sources/:id/activate"},
		{http.MethodPost, "/api/v1/admin/supplier-sources/:id/pause"},
		{http.MethodPost, "/api/v1/admin/supplier-sources/:id/models-discover"},
		{http.MethodGet, "/api/v1/admin/supplier-sources/:id/models-discover/jobs/:job_id"},
	} {
		require.False(t, registered[tt.method+" "+tt.path], "removed path=%s must not be registered", tt.path)
	}
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

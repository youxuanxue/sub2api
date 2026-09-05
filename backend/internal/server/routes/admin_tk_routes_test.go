//go:build unit

package routes

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterTKGroupMembershipRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	groups := router.Group("/api/v1/admin/groups")
	h := &handler.Handlers{
		Admin: &handler.AdminHandlers{
			Group: adminhandler.NewGroupHandler(nil, nil, nil),
		},
	}
	registerTKGroupMembershipRoutes(groups, h)

	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, tt := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/admin/groups/:id/accounts"},
		{http.MethodPost, "/api/v1/admin/groups/:id/accounts"},
		{http.MethodDelete, "/api/v1/admin/groups/:id/accounts"},
	} {
		require.True(t, registered[tt.method+" "+tt.path], "path=%s should be registered", tt.path)
	}
}

func TestApplyTKAdminComplianceMiddleware_NilSettingSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	admin := router.Group("/api/v1/admin")
	require.NotPanics(t, func() {
		applyTKAdminComplianceMiddleware(admin, nil)
	})
}

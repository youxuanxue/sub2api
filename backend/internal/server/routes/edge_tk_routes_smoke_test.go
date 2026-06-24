package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// routePath is the (method, path) tuple as gin records it in Engine.Routes().
type routePath struct {
	method string
	path   string
}

func registeredRoutes(t *testing.T, r *gin.Engine) map[routePath]bool {
	t.Helper()
	out := map[routePath]bool{}
	for _, ri := range r.Routes() {
		out[routePath{ri.Method, ri.Path}] = true
	}
	return out
}

// TestRegisterTKEdgeRoutes_OpsRegisteredNoConflict proves the edge account WRITE
// ops register cleanly alongside the existing inventory + admin-session routes —
// a gin/httprouter wildcard conflict would PANIC during registration, so reaching
// the assertions at all means the shared /accounts prefix and the :id wildcards
// coexist. It also pins the exact paths so a rename is caught.
func TestRegisterTKEdgeRoutes_OpsRegisteredNoConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	h := &handler.Handlers{
		EdgeCapacity:     &handler.EdgeCapacityHandler{},
		EdgeAccounts:     &handler.EdgeAccountsHandler{},
		EdgeAdminSession: &handler.EdgeAdminSessionHandler{},
		EdgeAccountOps:   &handler.EdgeAccountOpsHandler{},
	}

	require.NotPanics(t, func() {
		RegisterTKEdgeRoutes(v1, h, &service.APIKeyService{}, &service.UserService{})
	})

	got := registeredRoutes(t, r)
	for _, want := range []routePath{
		{"GET", "/api/v1/edge/accounts"},
		{"POST", "/api/v1/edge/admin-session"},
		{"PUT", "/api/v1/edge/caller-api-key/group"},
		{"POST", "/api/v1/edge/accounts/:id/clear-rate-limit"},
		{"POST", "/api/v1/edge/accounts/:id/reset-quota"},
		{"DELETE", "/api/v1/edge/accounts/:id/temp-unschedulable"},
		{"POST", "/api/v1/edge/accounts/:id/schedulable"},
		{"GET", "/api/v1/edge/accounts/:id/usage"},
	} {
		require.Truef(t, got[want], "edge route not registered: %s %s", want.method, want.path)
	}
}

// TestRegisterTKEdgeAccountsRoutes_ProxyOpsRegisteredNoConflict proves the prod
// admin proxy ops register alongside the read overview + admin-session under the
// shared :edge prefix without a wildcard conflict.
func TestRegisterTKEdgeAccountsRoutes_ProxyOpsRegisteredNoConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	adminGroup := r.Group("/api/v1/admin")

	h := &handler.Handlers{
		Admin: &handler.AdminHandlers{
			EdgeAccounts:   &adminhandler.EdgeAccountsHandler{},
			EdgeAccountOps: &adminhandler.EdgeAccountOpsHandler{},
		},
	}

	require.NotPanics(t, func() {
		registerTKEdgeAccountsRoutes(adminGroup, h)
	})

	got := registeredRoutes(t, r)
	for _, want := range []routePath{
		{"GET", "/api/v1/admin/edge-accounts"},
		{"POST", "/api/v1/admin/edge-accounts/:edge/admin-session"},
		{"POST", "/api/v1/admin/edge-accounts/:edge/accounts/:id/clear-rate-limit"},
		{"POST", "/api/v1/admin/edge-accounts/:edge/accounts/:id/reset-quota"},
		{"DELETE", "/api/v1/admin/edge-accounts/:edge/accounts/:id/temp-unschedulable"},
		{"POST", "/api/v1/admin/edge-accounts/:edge/accounts/:id/schedulable"},
		{"GET", "/api/v1/admin/edge-accounts/:edge/accounts/:id/usage"},
	} {
		require.Truef(t, got[want], "prod proxy route not registered: %s %s", want.method, want.path)
	}
}

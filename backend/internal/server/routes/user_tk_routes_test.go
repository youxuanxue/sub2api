package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterTKUserDualAuthRoutes_OnlyS3QABundleSurfaceRemains(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	eitherAuth := middleware.EitherAuthMiddleware(func(c *gin.Context) { c.Next() })

	registerTKUserDualAuthRoutes(v1, &handler.Handlers{QA: &handler.QAHandler{}}, eitherAuth, nil)

	got := registeredRoutes(t, r)
	for _, removed := range []routePath{
		{"POST", "/api/v1/users/me/qa/export"},
		{"GET", "/api/v1/users/me/qa/exports/*key"},
		{"POST", "/api/v1/users/me/qa/traj/export"},
		{"GET", "/api/v1/users/me/qa/traj/export/jobs"},
		{"GET", "/api/v1/users/me/qa/traj/export/jobs/:job_id"},
		{"GET", "/api/v1/users/me/qa/traj/exports/*key"},
	} {
		require.Falsef(t, got[removed], "retired QA self-export route is still registered: %s %s", removed.method, removed.path)
	}
	for _, kept := range []routePath{
		{"POST", "/api/v1/users/me/qa/bundles"},
		{"GET", "/api/v1/users/me/qa/bundles/:job_id"},
		{"POST", "/api/v1/users/me/qa/bundles/:job_id/export"},
		{"GET", "/api/v1/users/me/qa/bundle-exports/:job_id"},
	} {
		require.Truef(t, got[kept], "trajectory export route missing: %s %s", kept.method, kept.path)
	}
}

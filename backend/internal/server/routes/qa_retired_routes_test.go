package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRetiredQAProductRoutesReturn404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	authenticated := router.Group("/api/v1")
	registerTKUserRoutes(authenticated, authenticated.Group("/user"), &handler.Handlers{User: &handler.UserHandler{}})
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/users/me/qa/bundles"},
		{"GET", "/api/v1/users/me/qa/bundles/job"},
		{"POST", "/api/v1/users/me/qa/bundles/job/export"},
		{"GET", "/api/v1/users/me/qa/bundle-exports/job"},
		{"POST", "/api/v1/users/me/qa/export"},
		{"GET", "/api/v1/users/me/qa/exports/file"},
		{"POST", "/api/v1/users/me/qa/traj/export"},
		{"GET", "/api/v1/users/me/qa/traj/export/jobs"},
		{"GET", "/api/v1/users/me/qa/traj/export/jobs/job"},
		{"GET", "/api/v1/users/me/qa/traj/exports/file"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
			require.Equal(t, http.StatusNotFound, response.Code)
		})
	}
}

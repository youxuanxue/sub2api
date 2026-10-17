package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUS066_RetiredCursorRoutesReturn404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerAccountRoutes(router.Group("/api/v1/admin"), &handler.Handlers{
		Admin: &handler.AdminHandlers{Account: &admin.AccountHandler{}},
	}, func(c *gin.Context) { c.Next() })
	for _, route := range []struct{ method, path string }{
		{"GET", "/cursor/capabilities"},
		{"POST", "/cursor/authorizations"},
		{"GET", "/cursor/authorizations/session"},
		{"DELETE", "/cursor/authorizations/session"},
		{"POST", "/cursor/import"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(route.method, "/api/v1/admin/accounts"+route.path, nil))
			require.Equal(t, http.StatusNotFound, response.Code)
		})
	}
}

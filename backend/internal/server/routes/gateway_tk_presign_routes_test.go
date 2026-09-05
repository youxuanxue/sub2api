//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterTKOpenAICompatImagePresignRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/v1")
	registrar := newTerminalRouteRegistrar(v1, nil)
	h := &handler.Handlers{
		OpenAIGateway: &handler.OpenAIGatewayHandler{},
	}

	registerTKOpenAICompatImagePresignRoutes(registrar, h)

	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	require.True(t, registered[http.MethodPost+" /v1/images/presign"],
		"v1 images/presign must remain registered via companion")
}

func TestRegisterTKOpenAICompatImagePresignRoutesNoPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registrar := newTerminalRouteRegistrar(router, nil)
	h := &handler.Handlers{
		OpenAIGateway: &handler.OpenAIGatewayHandler{},
	}
	noop := gin.HandlerFunc(func(c *gin.Context) { c.Next() })

	registerTKOpenAICompatImagePresignRoutesNoPrefix(
		registrar, h, noop, noop, noop, noop, noop, noop, noop, noop,
	)

	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	require.True(t, registered[http.MethodPost+" /images/presign"],
		"no-prefix images/presign must remain registered via companion")

	// Hit the route: all 8 caller middleware must run before ImagesPresign.
	var hits int
	counting := gin.HandlerFunc(func(c *gin.Context) {
		hits++
		c.Next()
	})
	router2 := gin.New()
	registrar2 := newTerminalRouteRegistrar(router2, nil)
	registerTKOpenAICompatImagePresignRoutesNoPrefix(
		registrar2, h,
		counting, counting, counting, counting,
		counting, counting, counting, counting,
	)
	w := httptest.NewRecorder()
	router2.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/images/presign", nil))
	require.Equal(t, 8, hits, "no-prefix chain must keep all caller-supplied middleware")
}

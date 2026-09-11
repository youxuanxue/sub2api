package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Exercise RegisterGatewayRoutes through HTTP: scanning companion source alone
// cannot detect a removed call from the public router into that companion.
func TestGatewayCompanionRoutesRequireAPIKey(t *testing.T) {
	router, _, _ := newKeyBillingRouteTestRouter(config.RunModeStandard)
	type route struct{ method, path string }
	routes := []route{
		{http.MethodGet, "/openrouter/v1/models"},
		{http.MethodPost, "/openrouter/v1/images"},
		{http.MethodPost, "/openrouter/v1/videos"},
		{http.MethodGet, "/openrouter/v1/videos/task-id"},
	}
	for _, prefix := range []string{"", "/v1"} {
		for _, r := range []route{
			{http.MethodPost, "/tts"},
			{http.MethodPost, "/stt"},
			{http.MethodPost, "/custom-voices"},
			{http.MethodGet, "/custom-voices"},
			{http.MethodGet, "/custom-voices/voice-id/audio"},
			{http.MethodGet, "/custom-voices/voice-id"},
			{http.MethodPatch, "/custom-voices/voice-id"},
			{http.MethodDelete, "/custom-voices/voice-id"},
			{http.MethodGet, "/realtime"},
			{http.MethodPost, "/web_search"},
			{http.MethodPost, "/x_search"},
		} {
			routes = append(routes, route{r.method, prefix + r.path})
		}
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			req := httptest.NewRequest(r.method, r.path, strings.NewReader(`{"model":"grok-4"}`))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
			require.Contains(t, response.Header().Get("Content-Type"), "application/json")
		})
	}
}

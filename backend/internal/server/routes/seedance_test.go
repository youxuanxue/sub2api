package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSeedanceNativeRoutes(t *testing.T) {
	router := newGatewayRoutesTestRouter()
	for _, prefix := range []string{"/api/v3", "/v3", "/v1", ""} {
		for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
			path := prefix + "/contents/generations/tasks"
			if method != http.MethodPost {
				path += "/task-1"
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(`{"model":"seedance","content":[{"type":"text","text":"waves"}]}`)))
			require.NotEqual(t, http.StatusNotFound, w.Code, method+" "+path)
		}
	}
}

func TestSeedanceNewAPIGroupUsesVideoPipeline(t *testing.T) {
	w := httptest.NewRecorder()
	newGatewayRoutesTestRouter(service.PlatformNewAPI).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"doubao-seedance-2.0","content":[{"type":"text","text":"waves"}]}`)))
	require.NotContains(t, w.Body.String(), "Seedance requires an OpenAI or composite group")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "Video task registry is not configured")
}

func TestSeedanceRejectsOtherPlatforms(t *testing.T) {
	for _, platform := range []string{service.PlatformGrok, service.PlatformAnthropic, service.PlatformGemini} {
		w := httptest.NewRecorder()
		newGatewayRoutesTestRouter(platform).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"seedance","content":[{}]}`)))
		require.Equal(t, http.StatusForbidden, w.Code)
	}
}

//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUS050_CandidateDiscoveryAliasesDoNotRequirePayment(t *testing.T) {
	for _, universal := range []bool{false, true} {
		for _, path := range []string{"/v1/models", "/models", "/backend-api/codex/models", "/v1beta/models", "/v1beta/models/gemini-2.5-pro", "/antigravity/v1beta/models/gemini-2.5-pro"} {
			for _, google := range []bool{false, true} {
				api, _, key, _ := newCandidateHTTPServices(t, universal)
				key.User.Balance = 0
				cfg := &config.Config{RunMode: config.RunModeStandard}
				router := gin.New()
				if google {
					router.Use(APIKeyAuthGoogle(api, nil, cfg))
				} else {
					router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(api, nil, nil, cfg)))
				}
				router.GET(path, func(c *gin.Context) {
					require.Nil(t, service.CandidateRequestFromContext(c.Request.Context()))
					c.Status(http.StatusNoContent)
				})
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.Header.Set("Authorization", "Bearer candidate-key")
				request.Header.Set("x-goog-api-key", "candidate-key")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, request)
				require.Equal(t, http.StatusNoContent, w.Code, "universal=%t google=%t path=%s: %s", universal, google, path, w.Body.String())
			}
		}
	}
	for _, path := range []string{"/responses", "/v1beta/models/gemini-2.5-pro:generateContent", "/v1beta/models/gemini-2.5-pro/extra"} {
		require.False(t, skipsBillingEnforcement(http.MethodGet, path), path)
	}
	require.False(t, skipsBillingEnforcement(http.MethodPost, "/v1beta/models/gemini-2.5-pro"))
}

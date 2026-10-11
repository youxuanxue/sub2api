//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func googleQuotaRouter(apiKey *service.APIKey, cfg *config.Config) *gin.Engine {
	apiKeyService := newTestAPIKeyService(fakeAPIKeyRepo{
		getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			return &clone, nil
		},
	})
	settingService := service.NewSettingService(&complianceGuardRepoStub{}, cfg)
	r := gin.New()
	r.Use(APIKeyAuthWithSubscriptionGoogle(apiKeyService, nil, settingService, cfg))
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	r.POST("/v1beta/models/gemini-2.5-pro:generateContent", ok)
	r.GET("/v1beta/models", ok)
	return r
}

func googleQuotaKey() *service.APIKey {
	groupID := int64(101)
	user := &service.User{ID: 16, Role: service.RoleUser, Status: service.StatusActive, Balance: 10}
	return &service.APIKey{
		ID:        362,
		UserID:    user.ID,
		GroupID:   &groupID,
		Key:       "google-quota-exhausted",
		Status:    service.StatusAPIKeyQuotaExhausted,
		User:      user,
		Quota:     500,
		QuotaUsed: 500.04,
		Group: &service.Group{
			ID: groupID, Name: "gemini", Status: service.StatusActive,
			Platform: service.PlatformGemini, Hydrated: true,
		},
	}
}

// TestGoogleAPIKeyQuotaRejectionSkipsBodyAndSetsRetryAfter: the /v1beta ingress
// runs the same body read + candidate scheduling before its quota rejection, so
// it carries the same per-rejection cost and gets the same early exit.
func TestGoogleAPIKeyQuotaRejectionSkipsBodyAndSetsRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKey := googleQuotaKey()
	cfg := &config.Config{RunMode: config.RunModeStandard}
	router := googleQuotaRouter(apiKey, cfg)

	body := newCountingBody(`{"contents":[{"parts":[{"text":"hi"}]}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", body)
	req.Header.Set("x-goog-api-key", apiKey.Key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "60", rec.Header().Get("Retry-After"))
	require.Zero(t, body.reads.Load(), "request body was read before the quota rejection")

	// The Google error envelope is unchanged.
	var resp googleErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "api key 额度已用完", resp.Error.Message)
}

// TestGoogleAPIKeyQuotaExhaustedStillServesBillingExemptPaths: the metadata
// listing must stay reachable for an exhausted key on this ingress too.
func TestGoogleAPIKeyQuotaExhaustedStillServesBillingExemptPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKey := googleQuotaKey()
	cfg := &config.Config{RunMode: config.RunModeStandard}
	router := googleQuotaRouter(apiKey, cfg)

	require.True(t, skipsBillingEnforcement(http.MethodGet, "/v1beta/models"))

	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	req.Header.Set("x-goog-api-key", apiKey.Key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Header().Get("Retry-After"))
}

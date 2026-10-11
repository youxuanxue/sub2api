//go:build unit

package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// countingBody reports whether anything downstream of authentication actually
// consumed the request payload.
type countingBody struct {
	reader io.Reader
	reads  atomic.Int32
	closes atomic.Int32
}

func newCountingBody(payload string) *countingBody {
	return &countingBody{reader: strings.NewReader(payload)}
}

func (b *countingBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.reader.Read(p)
}

func (b *countingBody) Close() error {
	b.closes.Add(1)
	return nil
}

// quotaEarlyExitKey mirrors the prod shape that triggered this change: a
// universal key at its own ceiling (quota 500 / used 500.04), persisted as
// quota_exhausted.
func quotaEarlyExitKey() (*service.APIKey, *service.User) {
	user := &service.User{ID: 16, Role: service.RoleUser, Status: service.StatusActive, Balance: 10}
	group := &service.Group{ID: 8, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true}
	return &service.APIKey{
		ID:          362,
		UserID:      user.ID,
		Key:         "quota-exhausted-universal",
		Status:      service.StatusAPIKeyQuotaExhausted,
		RoutingMode: service.RoutingModeUniversal,
		User:        user,
		Group:       group,
		GroupID:     &group.ID,
		Quota:       500,
		QuotaUsed:   500.04,
	}, user
}

func quotaEarlyExitRepo(apiKey *service.APIKey, user *service.User, touches *atomic.Int32) *stubApiKeyRepo {
	return &stubApiKeyRepo{
		getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
			if key != apiKey.Key {
				return nil, service.ErrAPIKeyNotFound
			}
			clone := *apiKey
			userClone := *user
			clone.User = &userClone
			return &clone, nil
		},
		updateLastUsed: func(context.Context, int64, time.Time) error {
			if touches != nil {
				touches.Add(1)
			}
			return nil
		},
	}
}

// quotaEarlyExitRouter injects a real SettingService so the OpenRouter body
// rewrite (the first body reader after authentication) is live. Without the
// hoisted check this router reads the payload before rejecting.
func quotaEarlyExitRouter(apiKeyService *service.APIKeyService, cfg *config.Config) *gin.Engine {
	settingService := service.NewSettingService(&complianceGuardRepoStub{}, cfg)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(apiKeyService, nil, settingService, cfg)))
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	router.POST("/v1/messages", ok)
	router.POST("/v1/chat/completions", ok)
	router.POST("/v1/responses", ok)
	router.GET("/v1/usage", ok)
	router.GET("/v1/models", ok)
	return router
}

// TestAPIKeyQuotaRejectionDoesNotReadRequestBody is the cost regression: the
// 29.7k/day retry storm must not pay a body read (and the JSON parse and
// candidate scheduling behind it) per rejection.
func TestAPIKeyQuotaRejectionDoesNotReadRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKey, user := quotaEarlyExitKey()
	cfg := &config.Config{RunMode: config.RunModeStandard}
	var touches atomic.Int32
	svc := service.NewAPIKeyService(quotaEarlyExitRepo(apiKey, user, &touches), nil, nil, nil, nil, nil, cfg)
	router := quotaEarlyExitRouter(svc, cfg)

	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"} {
		body := newCountingBody(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
		req := httptest.NewRequest(http.MethodPost, path, body)
		req.Header.Set("x-api-key", apiKey.Key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusTooManyRequests, w.Code, path)
		require.Zero(t, body.reads.Load(), "%s: request body was read before the quota rejection", path)
		require.Zero(t, touches.Load(), "%s: rejected request must not touch last_used", path)
	}
}

// TestAPIKeyQuotaRejectionPreservesResponseShape pins the decision itself: the
// hoist must not change status, code or message on either protocol shape.
func TestAPIKeyQuotaRejectionPreservesResponseShape(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKey, user := quotaEarlyExitKey()
	cfg := &config.Config{RunMode: config.RunModeStandard}
	svc := service.NewAPIKeyService(quotaEarlyExitRepo(apiKey, user, nil), nil, nil, nil, nil, nil, cfg)
	router := quotaEarlyExitRouter(svc, cfg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5"}`))
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	requireAPIKeyAuthError(t, w, "API_KEY_QUOTA_EXHAUSTED", "API key 额度已用完")

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5"}`))
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Contains(t, w.Body.String(), "insufficient_quota")
}

// TestAPIKeyQuotaRejectionSetsRetryAfter: a well-behaved client needs a backoff
// hint, or it hot-loops the reject path (measured: ~356 req/min from one IP).
func TestAPIKeyQuotaRejectionSetsRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKey, user := quotaEarlyExitKey()
	cfg := &config.Config{RunMode: config.RunModeStandard}
	svc := service.NewAPIKeyService(quotaEarlyExitRepo(apiKey, user, nil), nil, nil, nil, nil, nil, cfg)
	router := quotaEarlyExitRouter(svc, cfg)

	// Both writers (legacy ErrorResponse and the OpenAI-compat body) must carry it.
	for _, path := range []string{"/v1/messages", "/v1/responses"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"m"}`))
		req.Header.Set("x-api-key", apiKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusTooManyRequests, w.Code, path)
		retryAfter := w.Header().Get("Retry-After")
		require.NotEmpty(t, retryAfter, "%s: quota 429 carries no Retry-After", path)
		require.Equal(t, "60", retryAfter, path)
	}
}

// TestAPIKeyQuotaRejectionRuntimeOverrunSetsRetryAfter covers the runtime
// branch: status still active, usage already past the ceiling.
func TestAPIKeyQuotaRejectionRuntimeOverrunSetsRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKey, user := quotaEarlyExitKey()
	apiKey.Status = service.StatusActive
	cfg := &config.Config{RunMode: config.RunModeStandard}
	svc := service.NewAPIKeyService(quotaEarlyExitRepo(apiKey, user, nil), nil, nil, nil, nil, nil, cfg)
	router := quotaEarlyExitRouter(svc, cfg)

	body := newCountingBody(`{"model":"claude-sonnet-4-5"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "60", w.Header().Get("Retry-After"))
	require.Zero(t, body.reads.Load(), "runtime quota overrun also skips the body read")
	requireAPIKeyAuthError(t, w, "API_KEY_QUOTA_EXHAUSTED", "API key 额度已用完")
}

// TestAPIKeyQuotaExhaustedStillServesBillingExemptPaths is the correctness
// guard on the hoist: skipsBillingEnforcement endpoints must keep working for
// an exhausted key, exactly as they did when the check lived in the billing
// block.
func TestAPIKeyQuotaExhaustedStillServesBillingExemptPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKey, user := quotaEarlyExitKey()
	cfg := &config.Config{RunMode: config.RunModeStandard}
	svc := service.NewAPIKeyService(quotaEarlyExitRepo(apiKey, user, nil), nil, nil, nil, nil, nil, cfg)
	router := quotaEarlyExitRouter(svc, cfg)

	for _, path := range []string{"/v1/usage", "/v1/models"} {
		require.True(t, skipsBillingEnforcement(http.MethodGet, path), "%s is expected to be billing-exempt", path)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("x-api-key", apiKey.Key)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code, "%s must stay reachable for an exhausted key", path)
		require.Empty(t, w.Header().Get("Retry-After"), "%s is not rate limited", path)
	}
}

// TestAPIKeyQuotaExhaustedSimpleModeStillBypasses: SimpleMode skips billing
// enforcement wholesale, so the hoisted check must not fire there either.
func TestAPIKeyQuotaExhaustedSimpleModeStillBypasses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	apiKey, user := quotaEarlyExitKey()
	// Direct routing: SimpleMode admits the request, and this harness has no
	// backing span store for the universal resolver to consult past admission.
	apiKey.RoutingMode = ""
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := service.NewAPIKeyService(quotaEarlyExitRepo(apiKey, user, nil), nil, nil, nil, nil, nil, cfg)
	router := quotaEarlyExitRouter(svc, cfg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

// TestAPIKeyQuotaRejectedPrecedence pins the predicate against the billing
// block it mirrors: an expired key must keep receiving the 403, not the 429.
func TestAPIKeyQuotaRejectedPrecedence(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	cases := []struct {
		name string
		key  *service.APIKey
		want bool
	}{
		{"nil key", nil, false},
		{"persisted quota_exhausted outranks expiry", &service.APIKey{Status: service.StatusAPIKeyQuotaExhausted, ExpiresAt: &past}, true},
		{"persisted expired loses to quota overrun", &service.APIKey{Status: service.StatusAPIKeyExpired, Quota: 1, QuotaUsed: 2}, false},
		{"runtime expiry loses to quota overrun", &service.APIKey{Status: service.StatusActive, ExpiresAt: &past, Quota: 1, QuotaUsed: 2}, false},
		{"runtime quota overrun while valid", &service.APIKey{Status: service.StatusActive, ExpiresAt: &future, Quota: 1, QuotaUsed: 1}, true},
		{"unlimited quota never rejected", &service.APIKey{Status: service.StatusActive, Quota: 0, QuotaUsed: 9999}, false},
		{"quota remaining", &service.APIKey{Status: service.StatusActive, Quota: 10, QuotaUsed: 9.9}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, apiKeyQuotaRejected(tc.key))
		})
	}
}

//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUS050_UniversalWebSocketDefersPaymentButEnforcesKeyLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name, path, mode, status string
		upgrade                  bool
		quota, used              float64
		expired                  bool
		want                     int
	}{
		{name: "subscription_only_can_reach_first_frame", path: "/v1/responses", mode: service.RoutingModeUniversal, upgrade: true, want: 200},
		{name: "root_responses", path: "/responses", mode: service.RoutingModeUniversal, upgrade: true, want: 200},
		{name: "codex_responses", path: "/openai/v1/responses", mode: service.RoutingModeUniversal, upgrade: true, want: 200},
		{name: "direct_keeps_payment_check", path: "/v1/responses", upgrade: true, want: 403},
		{name: "ordinary_get_keeps_payment_check", path: "/v1/responses", mode: service.RoutingModeUniversal, want: 403},
		{name: "unrelated_upgrade", path: "/other", mode: service.RoutingModeUniversal, upgrade: true, want: 403},
		{name: "disabled_key", path: "/v1/responses", mode: service.RoutingModeUniversal, upgrade: true, status: service.StatusDisabled, want: 401},
		{name: "expired_key", path: "/v1/responses", mode: service.RoutingModeUniversal, upgrade: true, expired: true, want: 403},
		{name: "quota_exhausted", path: "/v1/responses", mode: service.RoutingModeUniversal, upgrade: true, quota: 1, used: 1, want: 429},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := &service.APIKey{ID: 100, UserID: 7, Key: "test-key", Status: service.StatusActive,
				RoutingMode: test.mode, Quota: test.quota, QuotaUsed: test.used,
				User: &service.User{ID: 7, Status: service.StatusActive, Role: service.RoleUser}}
			if test.status != "" {
				key.Status = test.status
			}
			if test.expired {
				at := time.Now().Add(-time.Hour)
				key.ExpiresAt = &at
			}
			repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
			cfg := &config.Config{RunMode: config.RunModeStandard}
			api := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
			router := gin.New()
			router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(api, nil, nil, cfg)))
			reached := false
			router.GET(test.path, func(c *gin.Context) {
				reached = true
				requestKey, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				require.NotSame(t, key, requestKey)
				require.Nil(t, service.CandidateRequestFromContext(c.Request.Context()))
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req.Header.Set("Authorization", "Bearer test-key")
			if test.upgrade {
				req.Header.Set("Upgrade", "websocket")
				req.Header.Set("Connection", "Upgrade")
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, test.want, w.Code, w.Body.String())
			require.Equal(t, test.want == 200, reached)
		})
	}
}

type candidateHTTPAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r *candidateHTTPAccountRepo) ListCandidateAccounts(context.Context, []int64) ([]service.Account, error) {
	return append([]service.Account(nil), r.accounts...), nil
}

func newCandidateHTTPServices(t *testing.T, universal bool) (*service.APIKeyService, *service.GatewayService, *service.APIKey, *candidateHTTPAccountRepo) {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeStandard}
	groups := []service.Group{activeGroup(10, service.PlatformOpenAI), activeGroup(20, service.PlatformNewAPI)}
	for i := range groups {
		groups[i].Hydrated = true
		groups[i].AllowImageGeneration = true
		groups[i].RateMultiplier = 1
		groups[i].MessagesDispatchModelConfig.ExactModelMappings = map[string]string{"claude-sonnet-4-5": "claude-sonnet-4-5"}
		if universal {
			groups[i].MessagesDispatchModelConfig.ExactModelMappings["claude-sonnet-4-5"] = "unused-direct-alias"
		}
	}
	user := &service.User{ID: 7, Status: service.StatusActive, Role: service.RoleUser, Balance: 10}
	key := &service.APIKey{ID: 100, UserID: user.ID, Key: "candidate-key", Status: service.StatusActive, User: user}
	if universal {
		key.RoutingMode = service.RoutingModeUniversal
	} else {
		key.Group = &groups[0]
		key.GroupID = &groups[0].ID
	}
	keys := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	users := &stubUserRepo{getByID: func(context.Context, int64) (*service.User, error) { return user, nil }}
	groupRepo := &universalGroupRepoStub{active: groups}
	subs := &stubUserSubscriptionRepo{listActiveByUserID: func(context.Context, int64) ([]service.UserSubscription, error) { return nil, nil }}
	api := service.NewAPIKeyService(keys, users, groupRepo, subs, nil, nil, cfg)
	accounts := &candidateHTTPAccountRepo{}
	for i, group := range groups {
		accounts.accounts = append(accounts.accounts, service.Account{ID: int64(115 + i), Platform: service.PlatformAnthropic,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: i + 1, Concurrency: 10,
			Credentials: map[string]any{"base_url": "https://supplier.example", "api_key": "test-upstream-key", "model_mapping": map[string]string{"claude-sonnet-4-5": "claude-sonnet-4-5"}},
			GroupIDs:    []int64{group.ID}})
	}
	for i := range accounts.accounts {
		account := &accounts.accounts[i]
		identity, governed, err := service.BuildProtocolEndpointIdentity(account)
		require.NoError(t, err)
		require.True(t, governed)
		account.ProtocolEndpointCapabilityID = &account.ID
		account.ProtocolEndpointCapability = &service.ProtocolEndpointCapability{ID: account.ID, CapabilityKey: identity.Key(), Identity: identity,
			SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolMessages}, Revision: 1,
			ProbeEvidence: service.ProtocolProbeEvidence{InitialProbeCompleted: true}}
	}
	gw := service.NewGatewayService(accounts, groupRepo, nil, nil, nil, nil, nil, nil, cfg,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	openai := service.NewOpenAIGatewayService(accounts, nil, nil, nil, nil, nil, nil, cfg,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	service.ProvideTKUniversalModelsProvider(api, gw, nil, openai, service.NewProtocolRouter())
	return api, gw, key, accounts
}

func TestUS050_DefaultImageModelUsesCandidateAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, universal := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "universal"}[universal], func(t *testing.T) {
			api, _, key, accounts := newCandidateHTTPServices(t, universal)
			for i := range accounts.accounts {
				accounts.accounts[i].Platform = service.PlatformOpenAI
				accounts.accounts[i].Credentials["model_mapping"] = map[string]string{service.DefaultOpenAIImagesModel: service.DefaultOpenAIImagesModel}
			}
			for _, path := range []string{"/v1/images/generations", "/v1/images/edits"} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"prompt":"test"}`))
				c.Request.Header.Set("Content-Type", "application/json")
				require.False(t, MaybeResolveUniversal(c, key, api.UniversalResolver()))
				require.NotNil(t, service.CandidateRequestFromContext(c.Request.Context()))
				require.Equal(t, service.DefaultOpenAIImagesModel, service.CandidateEffectiveModel(c.Request.Context(), ""))
			}
		})
	}
}

func TestUS050_HTTPAuthCarriesActualCandidateAndRebindsBillingContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, universal := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "universal"}[universal], func(t *testing.T) {
			logs, stopLogs := captureMiddlewareStructuredLog(t)
			defer stopLogs()
			api, gateway, storedKey, _ := newCandidateHTTPServices(t, universal)
			cfg := &config.Config{RunMode: config.RunModeStandard}
			router := gin.New()
			router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(api, nil, nil, cfg)))
			router.POST("/v1/messages", func(c *gin.Context) {
				key, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				require.NotNil(t, service.CandidateRequestFromContext(c.Request.Context()))
				platform, selected := service.CandidateExecutionPlatform(c.Request.Context())
				require.True(t, selected)
				require.Equal(t, service.PlatformAnthropic, platform)
				require.Equal(t, service.PlatformOpenAI, key.Group.Platform)
				require.Equal(t, universal, service.IsUniversalKeyRouting(c.Request.Context()))
				session, present := service.CandidateSessionHash(c.Request.Context())
				require.True(t, present)
				require.NotEmpty(t, session)
				identityGroup, identityKey := service.CandidateAffinityCacheScope(c.Request.Context(), key.Group.ID, session)
				account, err := gateway.SelectAccountForModel(c.Request.Context(), key.GroupID, session, "claude-sonnet-4-5")
				require.NoError(t, err)
				require.Equal(t, int64(115), account.ID)
				if universal {
					next, err := gateway.SelectAccountForModelWithExclusions(c.Request.Context(), key.GroupID, session, "claude-sonnet-4-5", map[int64]struct{}{115: {}})
					require.NoError(t, err)
					require.Equal(t, int64(116), next.ID)
					require.Equal(t, int64(20), key.Group.ID)
					contextGroup, _ := c.Request.Context().Value(ctxkey.Group).(*service.Group)
					require.Equal(t, int64(20), contextGroup.ID)
					currentSession, _ := service.CandidateSessionHash(c.Request.Context())
					require.Equal(t, session, currentSession)
					nextIdentityGroup, nextIdentityKey := service.CandidateAffinityCacheScope(c.Request.Context(), key.Group.ID, currentSession)
					require.Equal(t, identityGroup, nextIdentityGroup)
					require.Equal(t, identityKey, nextIdentityKey)
				}
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`))
			req.Header.Set("Authorization", "Bearer candidate-key")
			req.Header.Set("x-session-id", "stable-session")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				for _, event := range logs.events {
					t.Logf("%s: %v", event.Message, event.Fields)
				}
			}
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			if universal {
				require.Nil(t, storedKey.Group)
			} else {
				require.Equal(t, int64(10), storedKey.Group.ID)
			}
		})
	}
}

func TestUS050_GoogleAuthUsesCandidateActualAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	api, _, storedKey, accounts := newCandidateHTTPServices(t, true)
	for i := range accounts.accounts {
		account := &accounts.accounts[i]
		account.Platform = service.PlatformGemini
		account.Credentials = map[string]any{"api_key": "test-key", "model_mapping": map[string]string{"gemini-2.5-flash": "gemini-2.5-flash"}}
		account.ProtocolEndpointCapability, account.ProtocolEndpointCapabilityID = nil, nil
	}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	router := gin.New()
	router.Use(APIKeyAuthWithSubscriptionGoogle(api, nil, nil, cfg))
	reached := false
	router.POST("/v1beta/models/:modelAction", func(c *gin.Context) {
		reached = true
		key, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.NotNil(t, service.CandidateRequestFromContext(c.Request.Context()))
		platform, ok := service.CandidateExecutionPlatform(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, service.PlatformGemini, platform)
		require.Equal(t, service.PlatformOpenAI, key.Group.Platform)
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	req.Header.Set("x-goog-api-key", "candidate-key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.True(t, reached)
	require.Nil(t, storedKey.Group)
}

func TestUS050_GoogleAuthKeepsKeyExpiryAndQuotaConstraints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name, status string
		expired      bool
		quota, used  float64
		want         int
	}{
		{name: "expired_status", status: service.StatusAPIKeyExpired, want: 403},
		{name: "expired_time", expired: true, want: 403},
		{name: "quota_status", status: service.StatusAPIKeyQuotaExhausted, want: 429},
		{name: "quota_usage", quota: 1, used: 1, want: 429},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := activeGroup(10, service.PlatformGemini)
			group.Hydrated = true
			key := &service.APIKey{ID: 100, UserID: 7, Key: "test-key", Status: service.StatusActive,
				Group: &group, GroupID: &group.ID, Quota: test.quota, QuotaUsed: test.used,
				User: &service.User{ID: 7, Status: service.StatusActive, Balance: 10}}
			if test.status != "" {
				key.Status = test.status
			}
			if test.expired {
				at := time.Now().Add(-time.Hour)
				key.ExpiresAt = &at
			}
			api := service.NewAPIKeyService(&stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}, nil, nil, nil, nil, nil, &config.Config{})
			router := gin.New()
			router.Use(APIKeyAuthWithSubscriptionGoogle(api, nil, nil, &config.Config{}))
			router.POST("/v1beta/models/:modelAction", func(c *gin.Context) { c.Status(http.StatusOK) })
			req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(`{"contents":[]}`))
			req.Header.Set("x-goog-api-key", "test-key")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, test.want, w.Code, w.Body.String())
		})
	}
}

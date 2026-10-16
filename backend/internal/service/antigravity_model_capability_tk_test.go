//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const antigravityDisabledModelBody = `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`

func TestAntigravityModelUnavailableNativeThinkingScope(t *testing.T) {
	for _, level := range []string{"low", "medium"} {
		t.Run(level, func(t *testing.T) {
			repo := &modelNotFoundAccountRepoStub{}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(antigravityDisabledModelBody))}}}
			svc := newAntigravityCompatService(config.GatewayConfig{}, upstream)
			svc.accountRepo = repo
			svc.rateLimitService = &RateLimitService{accountRepo: repo}
			account := newAntigravityCompatAccount(AccountTypeOAuth)
			account.Schedulable = true
			account.Credentials["model_mapping"] = map[string]any{"gemini-4-argon": "gemini-4-argon-high", "gemini-4-argon-low": "gemini-4-argon-low", "gemini-4-argon-medium": "gemini-4-argon-medium", "gemini-4-argon-high": "gemini-4-argon-high"}
			body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"` + level + `"}}}`)
			c, _ := newAntigravityCompatContext(http.MethodPost, "/v1beta/models/gemini-4-argon:generateContent", body)
			_, err := svc.ForwardGemini(context.Background(), c, account, "gemini-4-argon", "generateContent", false, body, false)
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.Len(t, upstream.requestBodies, 1)
			wire := gjson.GetBytes(upstream.requestBodies[0], "model").String()
			require.Equal(t, "gemini-4-argon-"+level, wire)
			require.Len(t, repo.modelRateLimitCalls, 1)
			call := repo.modelRateLimitCalls[0]
			require.Equal(t, wire, call.scope, "cool only the model actually sent upstream")
			setAccountModelRateLimitSnapshot(account, call.scope, call.resetAt, call.reason, time.Now())
			account.Extra["allow_overages"] = true
			resolver, _, _, _ := candidateGoogleFixture(t)
			ctx := resolver.WithRequest(context.Background(), ShapeGemini, "/v1beta/models/gemini-4-argon:generateContent", "gemini-4-argon", body)
			require.False(t, account.IsSchedulableForModelWithContext(ctx, "gemini-4-argon"))
			require.True(t, account.IsSchedulableForModel("gemini-4-argon-high"))
			c, _ = newAntigravityCompatContext(http.MethodPost, "/v1beta/models/gemini-4-argon:generateContent", body)
			_, err = svc.ForwardGemini(WithSingleAccountRetry(context.Background(), true, false), c, account, "gemini-4-argon", "generateContent", false, body, false)
			require.Error(t, err)
			require.Equal(t, 1, upstream.callCount, "credits and single-account retry cannot bypass the actual wire cooldown")
		})
	}
}

func TestAntigravityModelUnavailableClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       bool
	}{
		{"native disabled", antigravityDisabledModelBody, 404, true},
		{"legacy model error keeps existing policy", `{"error":{"message":"model not found"}}`, 404, false},
		{"route", `{"error":{"code":404,"message":"endpoint not found","status":"NOT_FOUND"}}`, 404, false},
		{"project", `{"error":{"code":404,"message":"Project not found","status":"NOT_FOUND"}}`, 404, false},
		{"html", `<html>404 Not Found</html>`, 404, false},
		{"missing status", `{"error":{"code":404,"message":"Requested entity was not found."}}`, 404, false},
		{"quota", antigravityDisabledModelBody, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isAntigravityModelUnavailableError(tc.status, []byte(tc.body)))
		})
	}
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	require.False(t, svc.HandleUpstreamModelNotFound(context.Background(), &Account{Platform: PlatformGemini, Type: AccountTypeOAuth}, "gemini-4-argon", 404, []byte(antigravityDisabledModelBody)))
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestAntigravityModelUnavailableAdapters(t *testing.T) {
	for _, wire := range []string{"gemini", "messages", "claude", "chat", "responses"} {
		t.Run(wire, func(t *testing.T) {
			repo := &modelNotFoundAccountRepoStub{}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(antigravityDisabledModelBody))}}}
			svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
			svc.settingService = NewSettingService(&antigravitySettingRepoStub{values: map[string]string{SettingKeyEnableModelFallback: "true", SettingKeyFallbackModelAntigravity: "gemini-3.8-flash-high"}}, &config.Config{})
			svc.accountRepo = repo
			svc.rateLimitService = &RateLimitService{accountRepo: repo}
			account := newAntigravityCompatAccount(AccountTypeOAuth)
			account.Schedulable = true
			account.Credentials["model_mapping"] = map[string]any{"gemini-4-argon": "gemini-4-argon-high", "gemini-3.8-flash": "gemini-3.8-flash-high", "claude-sonnet-4-6": "claude-sonnet-4-6"}
			model, resolved := "gemini-4-argon", "gemini-4-argon-high"
			if wire == "claude" {
				model, resolved = "claude-sonnet-4-6", "claude-sonnet-4-6"
			}
			body := []byte(`{"model":"` + model + `","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"contents":[{"role":"user","parts":[{"text":"hi"}]}],"input":"hi"}`)
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/test", body)
			var err error
			switch wire {
			case "gemini":
				_, err = svc.ForwardGemini(context.Background(), c, account, model, "generateContent", false, body, false)
			case "messages", "claude":
				_, err = svc.Forward(context.Background(), c, account, body, false)
			case "chat":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
			case "responses":
				_, err = svc.ForwardAsResponses(context.Background(), c, account, body, nil)
			}
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, http.StatusNotFound, failure.StatusCode)
			require.False(t, failure.RetryableOnSameAccount)
			require.True(t, classifyGatewayFailoverError(failure).RetryNextAccount)
			require.False(t, c.Writer.Written(), recorder.Body.String())
			require.Equal(t, 1, upstream.callCount)
			require.Len(t, repo.modelRateLimitCalls, 1)
			call := repo.modelRateLimitCalls[0]
			require.Equal(t, account.ID, call.accountID)
			require.Equal(t, resolved, call.scope)
			require.Equal(t, AntigravityModelUnavailableCooldownReason, call.reason)
			require.Zero(t, repo.tempCalls)
			require.Equal(t, StatusActive, account.Status)
			// Rehydrate the persisted cooldown as the scheduler does; credits must not
			// reopen an unavailable model, and unrelated Gemini capacity must survive.
			setAccountModelRateLimitSnapshot(account, call.scope, call.resetAt, call.reason, time.Now())
			account.Extra["allow_overages"] = true
			require.False(t, account.IsSchedulableForModel(model))
			require.True(t, account.IsSchedulableForModel("gemini-3.8-flash"))
			sibling := newAntigravityCompatAccount(AccountTypeOAuth)
			sibling.Schedulable = true
			sibling.Credentials["model_mapping"] = account.Credentials["model_mapping"]
			require.True(t, sibling.IsSchedulableForModel(model))
			_, retryErr := svc.TestConnection(context.Background(), account, model)
			require.Error(t, retryErr)
			require.Equal(t, 1, upstream.callCount, "credits cannot bypass the cooldown precheck")
			setAccountModelRateLimitSnapshot(account, call.scope, time.Now().Add(-time.Second), call.reason, time.Now())
			require.True(t, account.IsSchedulableForModel(model), "capability is reconsidered after cooldown")
		})
	}
}

func TestAntigravityModelUnavailableAdminTestDoesNotCoolAccount(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(antigravityDisabledModelBody))}}}
	svc := newAntigravityCompatService(config.GatewayConfig{}, upstream)
	svc.accountRepo = repo
	svc.rateLimitService = &RateLimitService{accountRepo: repo}
	account := newAntigravityCompatAccount(AccountTypeOAuth)
	account.Credentials["model_mapping"] = map[string]any{"gemini-4-argon": "gemini-4-argon-high"}
	_, err := svc.TestConnection(context.Background(), account, "gemini-4-argon")
	require.ErrorContains(t, err, "404")
	require.Equal(t, 1, upstream.callCount)
	require.Empty(t, repo.modelRateLimitCalls)
	require.Zero(t, repo.tempCalls)
}

type antigravityDiscoveryTransportFunc func(*http.Request) (*http.Response, error)

func (f antigravityDiscoveryTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestAntigravityDiscoveryUsesAccountServingEndpoint(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, tc := range []struct{ plan, override, endpoint string }{
		{"Pro", "", antigravity.DailyBaseURL()},
		{"Free", "", antigravity.ProdBaseURL()},
		{"Pro", "prod", antigravity.ProdBaseURL()},
	} {
		t.Run(tc.plan+"/"+tc.override, func(t *testing.T) {
			t.Setenv(antigravityForwardBaseURLEnv, tc.override)
			calls := 0
			http.DefaultTransport = antigravityDiscoveryTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, tc.endpoint+"/v1internal:fetchAvailableModels", r.URL.String())
				require.Equal(t, "Bearer fresh-oauth-token", r.Header.Get("Authorization"))
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":{"gemini-4-argon-disabled":{}}}`)), Request: r}, nil
			})
			gateway := newAntigravityCompatService(config.GatewayConfig{}, nil)
			svc := &AccountTestService{antigravityGatewayService: gateway}
			account := newAntigravityCompatAccount(AccountTypeOAuth)
			account.Credentials["plan_type"] = tc.plan
			models, err := svc.fetchAntigravityOAuthUpstreamModels(context.Background(), account)
			require.NoError(t, err)
			require.Equal(t, []string{"gemini-4-argon-disabled"}, models)
			require.Equal(t, 1, calls)
		})
	}
}

func TestAntigravityCatalogWithdrawalPreservesOtherSupplies(t *testing.T) {
	for _, model := range []string{"gemini-3.6-flash", "gemini-3.5-flash-lite"} {
		require.False(t, isPublicCatalogModelSupported(PlatformAntigravity, model))
		require.NotContains(t, supportedCatalogModelIDsForPlatform(PlatformAntigravity), model)
		require.True(t, isPublicCatalogModelSupported(PlatformGemini, model))
	}
	require.True(t, isPublicCatalogModelSupported(PlatformAntigravity, "gemini-4-argon"))
}

func TestAntigravityModelUnavailableCannotCascadeToWholeAccount(t *testing.T) {
	account := newAntigravityCompatAccount(AccountTypeOAuth)
	account.Schedulable = true
	now := time.Now()
	for i := 0; i <= AccountWideModelRateLimitThreshold; i++ {
		// Synthetic independent models isolate the cascade from catalog policy.
		setAccountModelRateLimitSnapshot(account, fmt.Sprintf("unavailable-%d", i), now.Add(time.Hour), AntigravityModelUnavailableCooldownReason, now)
	}
	require.True(t, account.IsSchedulableForModel("gemini-3.8-flash"))
	require.Nil(t, AccountWideRateLimitResetAt(account, now))
	// Actual quota cooldowns still obey the existing account-wide threshold.
	for i := 0; i <= AccountWideModelRateLimitThreshold; i++ {
		setAccountModelRateLimitSnapshot(account, fmt.Sprintf("quota-%d", i), now.Add(time.Hour), "quota_exhausted", now)
	}
	require.False(t, account.IsSchedulable())
	require.NotNil(t, AccountWideRateLimitResetAt(account, now))
}

//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestOAuthAdminTestRejectsUnavailableProxyBeforeNetwork(t *testing.T) {
	id := int64(7)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		ProxyID: &id, Credentials: map[string]any{"access_token": "test-token"}}
	upstream := &queuedHTTPUpstream{}
	svc := &AccountTestService{httpUpstream: upstream}
	c, recorder := newTestContext()
	err := svc.testAccountConnectionWithAccount(c, account, "gpt-5.4", "", "")
	require.ErrorContains(t, err, ErrOAuthProxyUnavailable.Error())
	require.Empty(t, upstream.requests)
	require.Contains(t, recorder.Body.String(), ErrOAuthProxyUnavailable.Error())
}

func TestOAuthQuotaRejectsUnavailableProxyBeforeNetwork(t *testing.T) {
	id := int64(7)
	expired := time.Now().Add(-time.Hour)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	oldURLs := antigravity.BaseURLs
	antigravity.BaseURLs = []string{server.URL}
	t.Cleanup(func() { antigravity.BaseURLs = oldURLs })
	for _, tc := range []struct {
		name  string
		proxy *Proxy
		err   error
	}{
		{name: "missing"},
		{name: "disabled", proxy: &Proxy{ID: id, Status: "inactive"}},
		{name: "expired", proxy: &Proxy{ID: id, Status: StatusActive, ExpiresAt: &expired}},
		{name: "mismatched", proxy: &Proxy{ID: 8, Status: StatusActive}},
		{name: "database failure", err: errors.New("database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxyRepo := &oauthBindingProxyRepo{proxy: tc.proxy, err: tc.err}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				ProxyID: &id, Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-org"}}
			repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{1: account}}
			factoryCalls := 0
			svc := NewOpenAIQuotaService(repo, proxyRepo, nil, func(string) (*req.Client, error) {
				factoryCalls++
				return nil, errors.New("unexpected network factory")
			}, nil)
			_, err := svc.QueryUsage(context.Background(), 1)
			require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
			require.Zero(t, factoryCalls)

			upstream := &queuedHTTPUpstream{}
			grok := NewGrokQuotaService(repo, proxyRepo, NewGrokTokenProvider(repo, nil), upstream, nil)
			account.Platform = PlatformGrok
			_, err = grok.ProbeUsage(context.Background(), 1)
			require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
			err = grok.syncGrokObservedModels(context.Background(), account)
			require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
			require.Empty(t, upstream.requests)

			account.Platform = PlatformAntigravity
			_, err = NewAntigravityQuotaFetcher(proxyRepo, nil).FetchQuota(context.Background(), account, "")
			require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
			require.Zero(t, requests.Load())

			account.Platform = PlatformOpenAI
			account.Proxy = tc.proxy
			_, err = (&AccountUsageService{}).probeOpenAICodexSnapshot(context.Background(), account)
			require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
		})
	}
}

func TestOAuthUnavailableProxyKeepsCachedUsageReadable(t *testing.T) {
	id := int64(7)
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, ProxyID: &id}
	cache := NewUsageCache()
	resp := &ClaudeUsageResponse{}
	resp.FiveHour.Utilization = 42
	cache.apiCache.Store(account.ID, &apiUsageCache{response: resp, timestamp: time.Now()})
	cache.windowStatsCache.Store(account.ID, &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	fetcher := &countingUsageFetcher{}
	svc := &AccountUsageService{cache: cache, usageFetcher: fetcher,
		accountRepo: &forceRefreshRepo{stubOpenAIAccountRepo{accounts: []Account{*account}}}}
	usage, err := svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, float64(42), usage.FiveHour.Utilization)
	require.Zero(t, fetcher.calls)
}

func TestOAuthAntigravityProxyFailureDoesNotClearCredentialError(t *testing.T) {
	id := int64(7)
	account := &Account{ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		ProxyID: &id, Status: StatusError, ErrorMessage: "token refresh failed",
		Credentials: map[string]any{"access_token": "test-token"}}
	repo := &openAIAccountTestRepo{}
	svc := &AccountUsageService{cache: NewUsageCache(), accountRepo: repo,
		antigravityQuotaFetcher: NewAntigravityQuotaFetcher(&oauthBindingProxyRepo{}, nil)}
	_, err := svc.GetUsageForAccount(context.Background(), account)
	require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
	require.Zero(t, repo.clearedErrorID)
	require.Equal(t, StatusError, account.Status)
}

func TestOAuthAnthropicUsageRejectsUnavailableProxyBeforeNetwork(t *testing.T) {
	id := int64(7)
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		ProxyID: &id, Credentials: map[string]any{"access_token": "test-token"}}
	fetcher := &countingUsageFetcher{resp: &ClaudeUsageResponse{}}
	svc := &AccountUsageService{usageFetcher: fetcher}
	_, err := svc.fetchOAuthUsageRaw(context.Background(), account)
	require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
	require.Zero(t, fetcher.calls)
}

func TestOAuthModelsRejectsUnavailableProxyBeforeNetwork(t *testing.T) {
	id := int64(7)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		ProxyID: &id, Credentials: map[string]any{"access_token": "test-token"}}
	upstream := &queuedHTTPUpstream{}
	svc := &AccountTestService{httpUpstream: upstream}
	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	_, err := svc.doUpstreamModelsRequest(req, "", account)
	require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
	_, err = (&OpenAIGatewayService{httpUpstream: upstream}).FetchCodexModelsManifest(context.Background(), account, "", "")
	require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
	require.Empty(t, upstream.requests)
}

func TestOAuthAdminTestAllowsConfiguredProxyAndExplicitDirect(t *testing.T) {
	id := int64(7)
	for _, pinned := range []bool{false, true} {
		account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "test-token"}}
		wantProxy := ""
		if pinned {
			account.ProxyID = &id
			account.Proxy = &Proxy{ID: id, Status: StatusActive, Protocol: "http", Host: "proxy.test", Port: 3128}
			wantProxy = account.Proxy.URL()
		}
		upstream := &httpUpstreamRecorder{err: errors.New("mock upstream rejection")}
		svc := &AccountTestService{httpUpstream: upstream}
		c, _ := newTestContext()
		err := svc.testAccountConnectionWithAccount(c, account, "gpt-5.4", "", "")
		require.ErrorContains(t, err, "mock upstream rejection")
		require.Len(t, upstream.requests, 1)
		require.Equal(t, wantProxy, upstream.lastProxyURL)
	}
}

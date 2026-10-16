//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type oauthBindingProxyRepo struct {
	ProxyRepository
	proxy *Proxy
	err   error
}

func (r *oauthBindingProxyRepo) GetByID(context.Context, int64) (*Proxy, error) {
	return r.proxy, r.err
}

func TestOAuthProxyBindingScheduling(t *testing.T) {
	id := int64(7)
	expired := time.Now().Add(-time.Hour)
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		for _, tc := range []struct {
			name    string
			id      *int64
			proxy   *Proxy
			allowed bool
		}{
			{"unbound direct", nil, nil, true}, {"missing", &id, nil, false},
			{"active", &id, &Proxy{ID: id, Status: StatusActive}, true},
			{"disabled", &id, &Proxy{ID: id, Status: "inactive"}, false},
			{"expired", &id, &Proxy{ID: id, Status: StatusActive, ExpiresAt: &expired}, false},
			{"mismatched", &id, &Proxy{ID: 8, Status: StatusActive}, false},
		} {
			t.Run(typ+"/"+tc.name, func(t *testing.T) {
				a := &Account{Type: typ, Status: StatusActive, Schedulable: true, ProxyID: tc.id, Proxy: tc.proxy}
				want := tc.allowed || typ == AccountTypeAPIKey
				require.Equal(t, want, a.IsSchedulable())
				require.Equal(t, want, a.IsCredentialUsableForShadow())
			})
		}
	}
}

func TestOAuthRefreshProxyResolver(t *testing.T) {
	id := int64(7)
	a := &Account{Type: AccountTypeOAuth, ProxyID: &id}
	active := &Proxy{ID: id, Status: StatusActive, Protocol: "socks5", Host: "127.0.0.1", Port: 1080}
	for _, tc := range []struct {
		name string
		repo ProxyRepository
		want string
		fail bool
	}{
		{"missing repository", nil, "", true},
		{"lookup failure", &oauthBindingProxyRepo{err: errors.New("database unavailable")}, "", true},
		{"missing proxy", &oauthBindingProxyRepo{}, "", true},
		{"disabled proxy", &oauthBindingProxyRepo{proxy: &Proxy{ID: id, Status: "inactive"}}, "", true},
		{"active proxy", &oauthBindingProxyRepo{proxy: active}, active.URL(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveOAuthAccountProxyURL(context.Background(), a, tc.repo)
			if tc.fail {
				require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.want, got)
		})
	}
	a.ProxyID = nil // An explicitly persisted direct fallback is permitted.
	got, err := resolveOAuthAccountProxyURL(context.Background(), a, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestOAuthProviderRefreshRejectsUnavailableProxyBeforeNetwork(t *testing.T) {
	id := int64(7)
	for _, state := range []struct {
		name  string
		proxy *Proxy
		err   error
	}{
		{name: "missing"}, {name: "disabled", proxy: &Proxy{ID: id, Status: "inactive"}},
		{name: "lookup failure", err: errors.New("lookup unavailable")},
	} {
		repo := &oauthBindingProxyRepo{proxy: state.proxy, err: state.err}
		providers := map[string]func(context.Context, *Account) error{
			PlatformAnthropic: func(ctx context.Context, a *Account) error {
				_, e := (&OAuthService{proxyRepo: repo}).RefreshAccountToken(ctx, a)
				return e
			},
			PlatformOpenAI: func(ctx context.Context, a *Account) error {
				_, e := (&OpenAIOAuthService{proxyRepo: repo}).RefreshAccountToken(ctx, a)
				return e
			},
			PlatformAntigravity: func(ctx context.Context, a *Account) error {
				_, e := (&AntigravityOAuthService{proxyRepo: repo}).RefreshAccountToken(ctx, a)
				return e
			},
			PlatformGemini: func(ctx context.Context, a *Account) error {
				_, e := (&GeminiOAuthService{proxyRepo: repo}).RefreshAccountToken(ctx, a)
				return e
			},
			PlatformGrok: func(ctx context.Context, a *Account) error {
				_, e := (&GrokOAuthService{proxyRepo: repo}).RefreshAccountToken(ctx, a)
				return e
			},
			PlatformKiro: func(ctx context.Context, a *Account) error { _, e := NewKiroTokenRefresher().Refresh(ctx, a); return e },
		}
		for platform, refresh := range providers {
			t.Run(platform+"/"+state.name, func(t *testing.T) {
				a := &Account{Platform: platform, Type: AccountTypeOAuth, ProxyID: &id, Proxy: state.proxy, Credentials: map[string]any{"refresh_token": "test-refresh"}}
				require.ErrorIs(t, refresh(context.Background(), a), ErrOAuthProxyUnavailable)
			})
		}
	}
}

func TestOAuthRefreshRereadsProxyBeforeExecutor(t *testing.T) {
	id := int64(7)
	selected := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, ProxyID: &id, Proxy: &Proxy{ID: id, Status: StatusActive}}
	fresh := *selected
	fresh.Proxy = nil
	repo := &refreshAPIAccountRepo{account: &fresh}
	executor := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{"access_token": "new"}}
	_, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(context.Background(), selected, executor, time.Hour)
	require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
	require.Zero(t, executor.refreshCalls)
	require.Zero(t, repo.updateCalls)
	fresh.Proxy = selected.Proxy
	result, err := NewOAuthRefreshAPI(repo, nil).RefreshIfNeeded(context.Background(), selected, executor, time.Hour)
	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Equal(t, 1, executor.refreshCalls)
}

func TestExecuteSelectedProtocolRejectsLostOAuthProxyBeforeExecutor(t *testing.T) {
	router := NewProtocolRouter()
	request := protocolRoutingTestRequest(t, protocolrouter.ProtocolMessages)
	ctx := WithProtocolRouting(context.Background(), router, request)
	account := protocolRoutingOpenAIAccount(12, "responses")
	account.Type = AccountTypeOAuth
	account.Credentials["access_token"] = "test-access"
	id := int64(7)
	account.ProxyID = &id
	account.Proxy = &Proxy{ID: id, Status: StatusActive}
	attachTestProtocolCapability(account, protocolrouter.ProtocolResponses)
	plan, _, err := protocolPlanForAccount(ctx, account, request.RequestedModel())
	require.NoError(t, err)
	fresh := *account
	fresh.Type = AccountTypeOAuth
	fresh.ProxyID = &id
	fresh.Proxy = nil
	calls := 0
	_, err = ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account,
		func(context.Context, *Account, string) error { return nil },
		func(context.Context, int64) (*Account, error) { return &fresh, nil },
		protocolExecutorsForTest(plan, func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
			calls++
			return nil, nil
		}))
	require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
	require.Zero(t, calls)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, GatewayFailureScopeAccount, failover.Scope)
	require.True(t, failover.ShouldRetryNextAccount())
}

type oauthBindingOpenAIClient struct {
	OpenAIOAuthClient
	calls    int
	proxyURL string
}

func (c *oauthBindingOpenAIClient) RefreshTokenWithClientID(_ context.Context, _ string, proxyURL string, _ string) (*openai.TokenResponse, error) {
	c.calls++
	c.proxyURL = proxyURL
	return nil, errors.New("test upstream reached")
}

func TestOAuthRefreshSendsConfiguredRouteAndExplicitDirect(t *testing.T) {
	id := int64(7)
	proxy := &Proxy{ID: id, Status: StatusActive, Protocol: "socks5", Host: "127.0.0.1", Port: 1080}
	for _, pinned := range []bool{true, false} {
		t.Run(map[bool]string{true: "pinned", false: "explicit direct"}[pinned], func(t *testing.T) {
			client := &oauthBindingOpenAIClient{}
			svc := &OpenAIOAuthService{proxyRepo: &oauthBindingProxyRepo{proxy: proxy}, oauthClient: client}
			a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "test"}}
			want := ""
			if pinned {
				a.ProxyID = &id
				want = proxy.URL()
			}
			_, err := svc.RefreshAccountToken(context.Background(), a)
			require.EqualError(t, err, "test upstream reached")
			require.Equal(t, 1, client.calls)
			require.Equal(t, want, client.proxyURL)
		})
	}
}

func TestOAuthRefreshPrivacyDoesNotBypassUnavailableProxy(t *testing.T) {
	id := int64(7)
	calls := 0
	svc := &TokenRefreshService{proxyRepo: &oauthBindingProxyRepo{err: errors.New("unavailable")}, privacyClientFactory: func(string) (*req.Client, error) { calls++; return nil, errors.New("must not reach network") }}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &id, Credentials: map[string]any{"access_token": "test"}}
	svc.ensureOpenAIPrivacy(context.Background(), account)
	require.Zero(t, calls)
}

func TestOAuthMetadataLookupDoesNotBypassUnavailableProxy(t *testing.T) {
	id := int64(7)
	repo := &oauthBindingProxyRepo{err: errors.New("database unavailable")}
	account := &Account{Type: AccountTypeOAuth, ProxyID: &id, Credentials: map[string]any{"access_token": "test", "oauth_type": "google_one"}}
	_, err := (&AntigravityOAuthService{proxyRepo: repo}).FillProjectID(context.Background(), account, "test")
	require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
	_, _, _, err = (&GeminiOAuthService{proxyRepo: repo}).RefreshAccountGoogleOneTier(context.Background(), account)
	require.ErrorIs(t, err, ErrOAuthProxyUnavailable)
}

func TestOAuthProxyFailureScopePreservesSharedInfrastructure(t *testing.T) {
	for _, tc := range []struct {
		err   error
		scope GatewayFailureScope
	}{
		{errors.Join(ErrOAuthProxyUnavailable, errOAuthProxyLookup), GatewayFailureScopeProvider},
		{errors.Join(ErrOAuthProxyUnavailable, errOAuthProxyRepository), GatewayFailureScopeProvider},
		{ErrOAuthProxyUnavailable, GatewayFailureScopeAccount},
	} {
		class := classifyGrokCredentialFailure(&Account{Type: AccountTypeOAuth, Platform: PlatformGrok}, tc.err)
		require.Equal(t, tc.scope, class.scope)
		require.False(t, class.permanent, "proxy failures must not invalidate OAuth credentials")
	}
}

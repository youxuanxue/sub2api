//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestClaudeResetUsesCanonicalUserAgentOwner(t *testing.T) {
	resetResolver(t)
	SetClaudeCodeUserAgentResolver(func(context.Context) string { return "9.9.9" })
	request, err := http.NewRequest(http.MethodGet, claudeResetUsageURL, nil)
	require.NoError(t, err)
	reset := &ClaudeResetCreditService{}
	reset.headers(context.Background(), request, "synthetic-token")
	require.Equal(t, GetCanonicalUserAgentForContext(context.Background()), request.Header.Get("User-Agent"))
	require.Equal(t, "Bearer synthetic-token", request.Header.Get("Authorization"))
}

type resetTLSUpstream struct {
	HTTPUpstream
	requests    []*http.Request
	proxy       string
	accountID   int64
	concurrency int
	profile     *tlsfingerprint.Profile
	err         error
}

func (upstream *resetTLSUpstream) DoWithTLS(request *http.Request, proxy string, accountID int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	upstream.requests = append(upstream.requests, request)
	upstream.proxy, upstream.accountID, upstream.concurrency, upstream.profile = proxy, accountID, concurrency, profile
	if upstream.err != nil {
		return nil, upstream.err
	}
	body := `{"cedar_ember":{"eligible":false,"grants":[]}}`
	switch request.URL.String() {
	case claudeResetProfileURL:
		body = `{"organization":{"uuid":"11111111-1111-4111-8111-111111111111"}}`
	case "https://api.anthropic.com/api/organizations/11111111-1111-4111-8111-111111111111/reset_rate_limits":
		body = `{"result":"reset","cleared":["five_hour"]}`
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestClaudeResetAccountTLSTransportAndSafety(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 3,
		Extra: map[string]any{"enable_tls_fingerprint": true, "tls_fingerprint_profile_id": 1}}
	profiles := newTLSSvcWithProfiles(&model.TLSFingerprintProfile{Name: canonicalTLSFingerprintProfileName, CipherSuites: []uint16{4865}})
	upstream := &resetTLSUpstream{}
	reset := NewClaudeResetCreditService(nil, nil, nil, profiles, upstream)
	proxy := "http://proxy.example.test:8080"
	ctx := context.Background()
	block, err := reset.fetchBlock(ctx, account, "synthetic-token", proxy)
	require.NoError(t, err)
	require.False(t, block.Eligible)
	org, err := reset.organization(ctx, account, "synthetic-token", proxy)
	require.NoError(t, err)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", org)
	outcome := reset.claim(ctx, account, "synthetic-token", proxy, org, "synthetic-grant", "synthetic-operation")
	require.Equal(t, ClaudeResetOutcomeReset, outcome.Outcome)
	require.Equal(t, []string{"five_hour"}, outcome.Cleared)
	require.Len(t, upstream.requests, 3)
	require.Equal(t, account.ID, upstream.accountID)
	require.Equal(t, 0, upstream.concurrency)
	require.Equal(t, proxy, upstream.proxy)
	require.Equal(t, canonicalTLSFingerprintProfileName, upstream.profile.Name)
	require.Equal(t, []uint16{4865}, upstream.profile.CipherSuites)
	for _, request := range upstream.requests {
		require.True(t, HTTPUpstreamRedirectsDisabled(request.Context()))
		require.True(t, HTTPUpstreamPublicHostsOnly(request.Context()))
		deadline, bounded := request.Context().Deadline()
		require.True(t, bounded)
		require.LessOrEqual(t, time.Until(deadline), 25*time.Second)
		require.Equal(t, "Bearer synthetic-token", request.Header.Get("Authorization"))
		require.Equal(t, GetCanonicalUserAgentForContext(ctx), request.Header.Get("User-Agent"))
	}
}

func TestClaudeResetTLSFailureDoesNotDowngradeTransport(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"enable_tls_fingerprint": true}}
	upstream := &resetTLSUpstream{err: errors.New("synthetic TLS failure")}
	reset := NewClaudeResetCreditService(nil, nil, nil, newTLSSvcWithProfiles(), upstream)
	plainCalls := 0
	reset.do = func(*http.Request, string) (*http.Response, error) {
		plainCalls++
		return nil, errors.New("unexpected plaintext transport")
	}
	_, err := reset.fetchBlock(context.Background(), account, "synthetic-token", "")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "synthetic-token")
	require.NotContains(t, err.Error(), "synthetic TLS failure")
	require.Zero(t, plainCalls)
	require.Len(t, upstream.requests, 1)

	reset.upstream = nil
	_, err = reset.fetchBlock(context.Background(), account, "synthetic-token", "")
	require.Error(t, err)
	require.Zero(t, plainCalls)
	account.Extra["enable_tls_fingerprint"] = false
	_, err = reset.fetchBlock(context.Background(), account, "synthetic-token", "")
	require.Error(t, err)
	require.Equal(t, 1, plainCalls)
}

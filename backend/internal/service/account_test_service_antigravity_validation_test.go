//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAccountTestAntigravityPersistsValidationChallenge(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		persist bool
	}{
		{"validation", 403, string(antigravityValidationBody()), true},
		{"ordinary forbidden", 403, `{"error":{"message":"permission denied"}}`, false},
		{"model unavailable", 404, antigravityDisabledModelBody, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}}}
			gateway := newAntigravityCompatService(config.GatewayConfig{}, upstream)
			gateway.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := newAntigravityCompatAccount(AccountTypeOAuth)
			svc := &AccountTestService{antigravityGatewayService: gateway, rateLimitService: gateway.rateLimitService}
			c, rec := newAntigravityCompatContext(http.MethodPost, "/admin/accounts/24/test", nil)
			err := svc.testAntigravityAccountConnection(c, account, "gemini-3.1-pro-high", "")
			require.Error(t, err)
			require.Contains(t, rec.Body.String(), `"type":"error"`)
			require.Equal(t, 0, repo.setErrorCalls)
			if tc.persist {
				require.Equal(t, 1, repo.tempCalls)
				require.Contains(t, repo.lastTempReason, "validation_url: https://accounts.google.com/verify")
			} else {
				require.Zero(t, repo.tempCalls)
			}
		})
	}
}

func TestAntigravityReadOnlyProbeDoesNotPersistValidationChallenge(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(antigravityValidationBody())))}}}
	gateway := newAntigravityCompatService(config.GatewayConfig{}, upstream)
	gateway.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	result, err := gateway.TestConnection(context.Background(), newAntigravityCompatAccount(AccountTypeOAuth), "gemini-3.1-pro-high")
	require.Error(t, err)
	require.Equal(t, 403, result.StatusCode)
	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.setErrorCalls)
}

func TestAccountTestAntigravityRefreshesURLWithoutEscalatingSameEpisode(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &stubAntigravityValidationCounter{count: 1}
	limits := newAntigravityValidationService(repo, counter)
	upstream := &queuedHTTPUpstreamStub{}
	gateway := newAntigravityCompatService(config.GatewayConfig{}, upstream)
	gateway.rateLimitService = limits
	svc := &AccountTestService{antigravityGatewayService: gateway, rateLimitService: limits}
	account := newAntigravityCompatAccount(AccountTypeOAuth)
	for _, token := range []string{"first", "latest"} {
		body := strings.ReplaceAll(string(antigravityValidationBody()), "https://accounts.google.com/verify", "https://accounts.google.com/verify?token="+token)
		upstream.responses = append(upstream.responses, &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))})
		c, _ := newAntigravityCompatContext(http.MethodPost, "/admin/accounts/24/test", nil)
		require.Error(t, svc.testAntigravityAccountConnection(c, account, "gemini-3.1-pro-high", ""))
		require.Equal(t, "https://accounts.google.com/verify?token="+token, repo.lastExtraUpdates["antigravity_validation_url"])
	}
	require.Equal(t, 1, repo.tempCalls, "a new link must not extend the cooldown")
	require.Equal(t, []int64{account.ID}, counter.incrementCalls)
	require.Zero(t, repo.setErrorCalls)
}

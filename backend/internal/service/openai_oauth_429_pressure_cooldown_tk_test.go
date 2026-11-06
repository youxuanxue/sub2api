//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func oauth429PressureAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{},
	}
}

type oauth429PressureRepo struct {
	capacityRetryAfterRepo
	setRateLimitedCalls int
}

func (r *oauth429PressureRepo) SetRateLimited(context.Context, int64, time.Time) error {
	r.setRateLimitedCalls++
	return nil
}

func (r *oauth429PressureRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}

func oauth429PressureService(repo *oauth429PressureRepo) *OpenAIGatewayService {
	svc := capacityRetryAfterService(&repo.capacityRetryAfterRepo)
	svc.rateLimitService.accountRepo = repo
	return svc
}

var oauth429PressureBody = []byte(`{"error":{"message":"Rate limit exceeded","type":"rate_limit_error"}}`)

func TestOpenAIOAuth429PressureModelCooldown_Duration(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		body    []byte
		want    time.Duration
	}{
		{name: "no headers uses default", want: openAIOAuth429PressureModelCooldownDefault},
		{name: "retry-after header", headers: http.Header{"Retry-After": []string{"20"}}, want: 20 * time.Second},
		{name: "retry-after over cap", headers: http.Header{"Retry-After": []string{"600"}}, want: openAIOAuth429PressureModelCooldownCap},
		{name: "retry-after under floor", headers: http.Header{"Retry-After": []string{"1"}}, want: openAIOAuth429PressureModelCooldownFloor},
		{
			name: "sub-100 codex reset-after is ignored",
			headers: http.Header{
				"X-Codex-Primary-Used-Percent":        []string{"42"},
				"X-Codex-Primary-Reset-After-Seconds": []string{"7200"},
				"X-Codex-Primary-Window-Minutes":      []string{"300"},
			},
			want: openAIOAuth429PressureModelCooldownDefault,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &oauth429PressureRepo{}
			svc := oauth429PressureService(repo)
			account := oauth429PressureAccount(501)
			body := tc.body
			if body == nil {
				body = oauth429PressureBody
			}

			start := time.Now()
			svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, tc.headers, body, "gpt-6.1-sol")

			require.Len(t, repo.modelCalls, 1)
			call := repo.modelCalls[0]
			require.Equal(t, account.ID, call.accountID)
			require.Equal(t, "gpt-6.1-sol", call.scope)
			require.Equal(t, openAIOAuth429PressureModelCooldownReason, call.reason)
			require.WithinDuration(t, start.Add(tc.want), call.resetAt, 2*time.Second)
			require.Zero(t, repo.overloadCalls)
			require.Zero(t, repo.tempUnschedCalls)
			require.Nil(t, account.RateLimitResetAt, "account-level window stays on the >=100% owner")
		})
	}
	require.Equal(t, 30*time.Second, openAIOAuth429PressureModelCooldownDefault)
	require.Equal(t, 60*time.Second, openAIOAuth429PressureModelCooldownCap)
}

func TestOpenAIOAuth429PressureModelCooldown_SkipsOutOfScope(t *testing.T) {
	apiKey := oauth429PressureAccount(502)
	apiKey.Type = AccountTypeAPIKey
	exhausted := http.Header{
		"X-Codex-Primary-Used-Percent":        []string{"100"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
		"X-Codex-Primary-Window-Minutes":      []string{"300"},
	}
	cases := []struct {
		name    string
		account *Account
		status  int
		headers http.Header
		body    []byte
		model   string
	}{
		{name: "api key account", account: apiKey, status: http.StatusTooManyRequests, body: oauth429PressureBody, model: "gpt-6.1-sol"},
		{name: "503 overloaded", account: oauth429PressureAccount(503), status: http.StatusServiceUnavailable, body: []byte(`{"error":{"message":"Rate limit exceeded"}}`), model: "gpt-6.1-sol"},
		{name: "other 429 wording", account: oauth429PressureAccount(504), status: http.StatusTooManyRequests, body: []byte(`{"error":{"message":"Too many requests"}}`), model: "gpt-6.1-sol"},
		{name: "usage exhausted 429", account: oauth429PressureAccount(505), status: http.StatusTooManyRequests, headers: exhausted, body: oauth429PressureBody, model: "gpt-6.1-sol"},
		{name: "no model", account: oauth429PressureAccount(506), status: http.StatusTooManyRequests, body: oauth429PressureBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &capacityRetryAfterRepo{}
			svc := capacityRetryAfterService(repo)
			require.False(t, svc.tkApplyOpenAIOAuth429PressureModelCooldown(context.Background(), tc.account, tc.status, tc.headers, tc.body, tc.model))
			require.Empty(t, repo.modelCalls)
		})
	}
}

func TestOpenAIOAuth429PressureModelCooldown_SchedulerSkipsOnlyThatModel(t *testing.T) {
	repo := &oauth429PressureRepo{}
	svc := oauth429PressureService(repo)
	account := oauth429PressureAccount(507)

	svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, nil, oauth429PressureBody, "gpt-6.1-sol")

	require.Len(t, repo.modelCalls, 1)
	require.True(t, account.IsSchedulable(), "the account itself stays schedulable")
	require.Nil(t, account.TempUnschedulableUntil)
	require.Nil(t, account.OverloadUntil)
	require.False(t, account.IsSchedulableForModel("gpt-6.1-sol"))
	require.True(t, account.IsSchedulableForModel("gpt-5.4"))

	// A fresh service has no in-memory block, so only the persisted model
	// cooldown can exclude the account (cross-instance view).
	peer := oauth429PressureService(&oauth429PressureRepo{})
	peer.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*account}}
	cooled, err := peer.SelectAccountForModelWithExclusions(context.Background(), nil, "", "gpt-6.1-sol", nil)
	require.Error(t, err)
	require.Nil(t, cooled)
	other, err := peer.SelectAccountForModelWithExclusions(context.Background(), nil, "", "gpt-5.4", nil)
	require.NoError(t, err)
	require.NotNil(t, other)
	require.Equal(t, account.ID, other.ID)
}

func TestShouldStopOpenAIOAuth429Failover_StormCutsBudget(t *testing.T) {
	cases := []struct {
		name      string
		recorded  int
		stopAfter int
	}{
		{name: "below threshold keeps three attempts", recorded: openAIOAuth429StormThreshold - 1, stopAfter: openAIOAuth429MaxAccountAttempts},
		{name: "at threshold stops after one switch", recorded: openAIOAuth429StormThreshold, stopAfter: 1 + openAIOAuth429StormMaxAccountSwitches},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			for i := 0; i < tc.recorded; i++ {
				svc.recordOpenAIOAuth429()
			}
			account := oauth429PressureAccount(508)
			apiKey := oauth429PressureAccount(509)
			apiKey.Type = AccountTypeAPIKey
			var state OpenAIOAuth429FailoverState
			for switches := 1; switches < tc.stopAfter; switches++ {
				require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, switches, &state), "switch %d", switches)
			}
			require.True(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, tc.stopAfter, &state))
			require.False(t, svc.ShouldStopOpenAIOAuth429Failover(apiKey, http.StatusTooManyRequests, tc.stopAfter, &state))
			require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusServiceUnavailable, tc.stopAfter, &state))
		})
	}
}

func TestOpenAIOAuth429StormActive_ExpiresWithWindow(t *testing.T) {
	svc := &OpenAIGatewayService{}
	for i := 0; i < openAIOAuth429StormThreshold; i++ {
		svc.recordOpenAIOAuth429()
	}
	require.True(t, svc.openAIOAuth429StormActive(time.Now()))
	require.False(t, svc.openAIOAuth429StormActive(time.Now().Add(openAIOAuth429StormWindow)))
}

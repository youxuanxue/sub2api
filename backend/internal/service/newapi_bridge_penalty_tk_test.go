//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// tkBridgePenaltyIncidentRecorder records NotifyAccountIncident calls so tests
// can assert the Feishu funnel fires with the upstream detail attached.
type tkBridgePenaltyIncidentRecorder struct {
	reasons   []string
	details   []string
	recovered []int64
}

func (r *tkBridgePenaltyIncidentRecorder) NotifyAccountIncident(account *Account, until time.Time, reason string, kind AccountIncidentKind, detail ...string) {
	r.reasons = append(r.reasons, reason)
	d := ""
	if len(detail) > 0 {
		d = detail[0]
	}
	r.details = append(r.details, d)
}

func (r *tkBridgePenaltyIncidentRecorder) NotifyAccountRecovered(accountID int64) {
	r.recovered = append(r.recovered, accountID)
}

func newBridgePenaltyTestService() (*RateLimitService, *rateLimitAccountRepoStub, *runtimeBlockRecorder, *tkBridgePenaltyIncidentRecorder) {
	repo := &rateLimitAccountRepoStub{}
	blocker := &runtimeBlockRecorder{}
	incidents := &tkBridgePenaltyIncidentRecorder{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAccountRuntimeBlocker(blocker)
	svc.SetAccountIncidentNotifier(incidents)
	return svc, repo, blocker, incidents
}

func newNewAPIBridgeAccount() *Account {
	// Mirrors prod account 39 "ds-官": fifth-platform newapi apikey account
	// with a DeepSeek channel type, no pool mode, no custom error codes.
	return &Account{
		ID:          39,
		Name:        "ds-官",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 43,
	}
}

func upstreamBridgeError(statusCode int, msg string) *newapitypes.NewAPIError {
	return newapitypes.NewErrorWithStatusCode(
		errors.New(msg),
		newapitypes.ErrorCodeBadResponseStatusCode,
		statusCode,
	)
}

// 402 insufficient balance MUST disable the account (handleAuthError path) and
// surface the upstream verdict in the incident detail — the 2026-06-11 prod
// gap where 6946×402 left the account schedulable with no alert.
func TestTkHandleBridgeUpstreamPenalty_402DisablesAccountAndAlerts(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := newNewAPIBridgeAccount()

	tkHandleBridgeUpstreamPenalty(context.Background(), svc, account, upstreamBridgeError(402, "Insufficient Balance"))

	require.Equal(t, 1, repo.setErrorCalls, "402 must SetError (stop scheduling)")
	require.Contains(t, repo.lastErrorMsg, "Payment required (402)")
	require.Contains(t, repo.lastErrorMsg, "Insufficient Balance")

	require.Equal(t, []string{"auth_error"}, blocker.reasons)
	require.Equal(t, []string{"auth_error"}, incidents.reasons)
	require.Len(t, incidents.details, 1)
	require.Contains(t, incidents.details[0], "402", "Feishu detail must carry the upstream status")
	require.Contains(t, incidents.details[0], "Insufficient Balance")
}

// 429 must put the account into a rate-limit cooldown via the fallback path
// (no reset headers available on the bridge path → apply429FallbackRateLimit).
func TestTkHandleBridgeUpstreamPenalty_429SetsRateLimitCooldown(t *testing.T) {
	svc, repo, _, _ := newBridgePenaltyTestService()
	account := newNewAPIBridgeAccount()

	tkHandleBridgeUpstreamPenalty(context.Background(), svc, account, upstreamBridgeError(429, "Requests rate limit exceeded"))

	require.Equal(t, 1, repo.setRateLimitedCalls, "429 must SetRateLimited (fallback cooldown)")
	require.True(t, repo.lastRateLimitedResetAt.After(time.Now()), "cooldown reset must be in the future")
	require.Zero(t, repo.setErrorCalls, "429 must not permanently disable")
}

// 401 on an apikey account must disable (invalid credentials).
func TestTkHandleBridgeUpstreamPenalty_401DisablesAccount(t *testing.T) {
	svc, repo, _, _ := newBridgePenaltyTestService()
	account := newNewAPIBridgeAccount()

	tkHandleBridgeUpstreamPenalty(context.Background(), svc, account, upstreamBridgeError(401, "Authentication Fails"))

	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, "Authentication failed (401)")
}

// Client-induced statuses must NEVER penalize the account (the #617 lesson:
// cooling accounts on client 400s drains the pool). 404 model-not-found and
// the synthetic 400 bridge errors are all out of the allowlist.
func TestTkHandleBridgeUpstreamPenalty_ClientInducedStatusesAreIgnored(t *testing.T) {
	for _, tc := range []struct {
		name   string
		apiErr *newapitypes.NewAPIError
	}{
		{"client 400", upstreamBridgeError(400, "The supported API model names are ...")},
		{"model not found 404", upstreamBridgeError(404, "model_not_found")},
		{"synthetic missing credential", errBridgeMissingCredential("api_key")},
		{"synthetic unsupported video channel", errBridgeVideoUnsupportedChannel(1)},
		{"server 500", upstreamBridgeError(500, "internal error")},
		{"server 502", upstreamBridgeError(502, "bad gateway")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, blocker, incidents := newBridgePenaltyTestService()
			account := newNewAPIBridgeAccount()

			tkHandleBridgeUpstreamPenalty(context.Background(), svc, account, tc.apiErr)

			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.setRateLimitedCalls)
			require.Zero(t, repo.tempCalls)
			require.Empty(t, blocker.reasons)
			require.Empty(t, incidents.reasons)
		})
	}
}

// nil inputs must be no-ops (defensive: penalty is best-effort glue, never a
// crash source on the error path).
func TestTkHandleBridgeUpstreamPenalty_NilSafety(t *testing.T) {
	svc, repo, _, _ := newBridgePenaltyTestService()
	account := newNewAPIBridgeAccount()

	tkHandleBridgeUpstreamPenalty(context.Background(), nil, account, upstreamBridgeError(402, "x"))
	tkHandleBridgeUpstreamPenalty(context.Background(), svc, nil, upstreamBridgeError(402, "x"))
	tkHandleBridgeUpstreamPenalty(context.Background(), svc, account, nil)

	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setRateLimitedCalls)
}

// The penalty write must survive client cancellation: a canceled request
// context must not abort the SetError write (context.WithoutCancel inside
// openAIAccountStateContext).
func TestTkHandleBridgeUpstreamPenalty_SurvivesCanceledRequestContext(t *testing.T) {
	svc, repo, _, _ := newBridgePenaltyTestService()
	account := newNewAPIBridgeAccount()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tkHandleBridgeUpstreamPenalty(ctx, svc, account, upstreamBridgeError(402, "Insufficient Balance"))

	require.Equal(t, 1, repo.setErrorCalls)
}

func TestTkBridgeUpstreamErrorBody_OpenAIEnvelope(t *testing.T) {
	body := tkBridgeUpstreamErrorBody(upstreamBridgeError(402, "Insufficient Balance"))
	require.NotEmpty(t, body)
	require.Contains(t, string(body), `"error"`)
	require.Contains(t, string(body), "Insufficient Balance")
	require.Nil(t, tkBridgeUpstreamErrorBody(nil))
}

// The permanent Feishu card must render the upstream detail line when present
// and keep the legacy shape when absent.
func TestBuildAccountIncidentPermanentText_RendersUpstreamDetail(t *testing.T) {
	account := newNewAPIBridgeAccount()
	cls := classifyIncident("auth_error", time.Time{}, IncidentKindUnknown)
	now := time.Now()

	withDetail := buildAccountIncidentPermanentText("prod", account, "auth_error", cls, now, "Payment required (402): Insufficient Balance")
	require.Contains(t, withDetail, "**详情**：")
	require.Contains(t, withDetail, "Payment required (402)")
	require.Contains(t, withDetail, "ds-官")
	require.Contains(t, withDetail, PlatformNewAPI)

	withoutDetail := buildAccountIncidentPermanentText("prod", account, "auth_error", cls, now, "")
	require.False(t, strings.Contains(withoutDetail, "**详情**"), "no detail line when detail is empty")
}

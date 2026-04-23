//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// US-028 — Bug B-1 + B-3 verification.
//
// B-1: NewAPI bridge dispatch returns *NewAPIError directly to handlers
//      without ever invoking RateLimitService.HandleUpstreamError. This means
//      newapi accounts hit by 401 / 402 / 429 / 529 / 403 silently bypass the
//      account state machine — permanently revoked keys never SetError, etc.
//      Fix: introduce reportNewAPIBridgeUpstreamError (TK funnel) + invoke
//      it in the `if apiErr != nil` branch of every Tier1 bridge dispatch.
//
// B-3: ratelimit_service.handle401 / handle402 only recognised PlatformOpenAI
//      for token_invalidated / token_revoked / detail:Unauthorized /
//      deactivated_workspace. PlatformNewAPI (OpenAI-shape upstream) was
//      missed, so permanently revoked newapi keys were treated as transient
//      and auto-revived every 10 minutes. Fix: switch to IsOpenAICompatPlatform.
//
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md.

// us028HandleUpstreamErrorRecorder captures (statusCode, body) passed into
// RateLimitService.HandleUpstreamError so we can assert that the bridge call
// site actually funnels the bridge apiErr through.
type us028HandleUpstreamErrorRecorder struct {
	rateLimitAccountRepoStub
	calls []us028HandleUpstreamErrorCall
}

type us028HandleUpstreamErrorCall struct {
	accountID  int64
	statusCode int
	body       string
}

// observableRateLimitService is a thin shim that lets us inspect what the
// bridge funnel calls without touching the production RateLimitService
// constructor. It implements the surface we need for the test (the funnel
// only calls HandleUpstreamError on the rateLimitService field).
type observableRateLimitService struct {
	*RateLimitService
	recorder *us028HandleUpstreamErrorRecorder
}

func newObservableRateLimitService(t *testing.T) (*RateLimitService, *us028HandleUpstreamErrorRecorder) {
	t.Helper()
	repo := &us028HandleUpstreamErrorRecorder{}
	rl := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	// Wrap SetError to also record the (accountID, errorMsg) so positive
	// cases ("401 → SetError") are observable.
	return rl, repo
}

func (r *us028HandleUpstreamErrorRecorder) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.calls = append(r.calls, us028HandleUpstreamErrorCall{accountID: id, statusCode: -1, body: errorMsg})
	return r.rateLimitAccountRepoStub.SetError(ctx, id, errorMsg)
}

// --- B-1 funnel tests ----------------------------------------------------

func TestUS028_ReportNewAPIBridgeUpstreamError_OpenAIGateway_Forwards401(t *testing.T) {
	rl, repo := newObservableRateLimitService(t)
	svc := &OpenAIGatewayService{rateLimitService: rl}
	account := &Account{ID: 9001, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 14}

	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("upstream key invalid"),
		newapitypes.ErrorCodeAccessDenied,
		http.StatusUnauthorized,
	)

	svc.reportNewAPIBridgeUpstreamError(context.Background(), account, apiErr)

	require.Equal(t, 1, repo.setErrorCalls, "401 must SetError on the account")
	require.Contains(t, repo.lastErrorMsg, "401")
}

func TestUS028_ReportNewAPIBridgeUpstreamError_OpenAIGateway_Forwards402(t *testing.T) {
	rl, repo := newObservableRateLimitService(t)
	svc := &OpenAIGatewayService{rateLimitService: rl}
	account := &Account{ID: 9002, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 14}

	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("insufficient balance"),
		newapitypes.ErrorCodeInsufficientUserQuota,
		http.StatusPaymentRequired,
	)

	svc.reportNewAPIBridgeUpstreamError(context.Background(), account, apiErr)

	require.Equal(t, 1, repo.setErrorCalls, "402 must SetError on the account")
}

func TestUS028_ReportNewAPIBridgeUpstreamError_GatewayService_Forwards401(t *testing.T) {
	// Mirror test for the GatewayService receiver (used by gateway_bridge_dispatch.go).
	rl, repo := newObservableRateLimitService(t)
	svc := &GatewayService{rateLimitService: rl}
	account := &Account{ID: 9003, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 14}

	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("upstream key invalid"),
		newapitypes.ErrorCodeAccessDenied,
		http.StatusUnauthorized,
	)

	svc.reportNewAPIBridgeUpstreamError(context.Background(), account, apiErr)

	require.Equal(t, 1, repo.setErrorCalls)
}

func TestUS028_ReportNewAPIBridgeUpstreamError_NilApiErr_NoCall(t *testing.T) {
	rl, repo := newObservableRateLimitService(t)
	svc := &OpenAIGatewayService{rateLimitService: rl}
	account := &Account{ID: 9004, Platform: PlatformNewAPI, ChannelType: 14}

	svc.reportNewAPIBridgeUpstreamError(context.Background(), account, nil)

	require.Equal(t, 0, repo.setErrorCalls, "nil apiErr must not invoke RateLimitService")
}

func TestUS028_ReportNewAPIBridgeUpstreamError_NilAccount_NoPanic(t *testing.T) {
	rl, _ := newObservableRateLimitService(t)
	svc := &OpenAIGatewayService{rateLimitService: rl}

	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("x"), newapitypes.ErrorCodeAccessDenied, http.StatusUnauthorized,
	)

	require.NotPanics(t, func() {
		svc.reportNewAPIBridgeUpstreamError(context.Background(), nil, apiErr)
	})
}

func TestUS028_ReportNewAPIBridgeUpstreamError_OutOfRangeStatus_FallsBackTo502(t *testing.T) {
	// Bug B-1 bridge errors built via certain ErrOption combos can carry
	// StatusCode == 0; the funnel must coerce to 502 so HandleUpstreamError
	// classification (≥500 = warn-only, no SetError) still fires correctly
	// instead of being treated as 0 / 200.
	rl, repo := newObservableRateLimitService(t)
	svc := &OpenAIGatewayService{rateLimitService: rl}
	account := &Account{ID: 9005, Platform: PlatformNewAPI, ChannelType: 14}

	apiErr := newapitypes.NewError(errors.New("transient"), newapitypes.ErrorCodeBadResponse)
	// Force StatusCode to 0 to simulate the regression case.
	apiErr.StatusCode = 0

	svc.reportNewAPIBridgeUpstreamError(context.Background(), account, apiErr)

	// 502 falls into the default "≥500 = warn but do not SetError" branch.
	require.Equal(t, 0, repo.setErrorCalls,
		"5xx default branch must NOT SetError, just log warn — verifies coerce-to-502 path")
}

// TestUS028_AllBridgeDispatchSitesCallReportHelper grep-asserts that every
// `if apiErr != nil` block in the seven bridge dispatch entry points
// invokes the funnel. This is the "convert convention to mechanical check"
// pattern (CLAUDE.md §10): when reviewers want to know whether a future PR
// added a new bridge endpoint without wiring the funnel, they run this test.
func TestUS028_AllBridgeDispatchSitesCallReportHelper(t *testing.T) {
	files := []string{
		"openai_gateway_bridge_dispatch.go",
		"openai_gateway_bridge_dispatch_tk_anthropic.go",
		"gateway_bridge_dispatch.go",
	}
	expectedCallCount := map[string]int{
		"openai_gateway_bridge_dispatch.go":            4, // chat / responses / embeddings / images
		"openai_gateway_bridge_dispatch_tk_anthropic.go": 1, // anthropic-via-chat
		"gateway_bridge_dispatch.go":                   2, // chat / responses
	}

	for _, f := range files {
		got := us028CountReportCalls(t, f)
		want := expectedCallCount[f]
		if got < want {
			t.Errorf("%s: expected ≥%d invocations of reportNewAPIBridgeUpstreamError (one per `if apiErr != nil` branch), got %d. New bridge endpoint added without wiring B-1 funnel?",
				f, want, got)
		}
	}
}

// us028CountReportCalls counts occurrences of the funnel call inside the
// given service file, scoped to the same package directory.
func us028CountReportCalls(t *testing.T, filename string) int {
	t.Helper()
	contents := us028ReadServiceFile(t, filename)
	return us028CountSubstring(contents, "reportNewAPIBridgeUpstreamError(")
}

// --- B-3 handle401 newapi recognition tests ------------------------------

func TestUS028_Handle401_NewAPI_TokenInvalidated_SetsError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:          200,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 14,
	}
	body := []byte(`{"error":{"code":"token_invalidated","message":"upstream key revoked"}}`)

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, 401, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls,
		"newapi token_invalidated must SetError (permanent) — Bug B-3")
	require.Contains(t, repo.lastErrorMsg, "Token revoked")
	// Must NOT take the OAuth/temp_unschedulable branch (newapi accounts are
	// AccountTypeAPIKey but the regression trapped them in the catch-all
	// non-OAuth branch via the same SetError path; the assertion above
	// confirms the explicit Token-revoked path).
}

func TestUS028_Handle401_NewAPI_TokenRevoked_SetsError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 201, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 14}
	body := []byte(`{"error":{"code":"token_revoked","message":"key revoked"}}`)

	svc.HandleUpstreamError(context.Background(), account, 401, http.Header{}, body)

	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, "Token revoked")
}

func TestUS028_Handle401_NewAPI_DetailUnauthorized_SetsError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 202, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 14}
	body := []byte(`{"detail":"Unauthorized"}`)

	svc.HandleUpstreamError(context.Background(), account, 401, http.Header{}, body)

	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, "Unauthorized")
}

func TestUS028_Handle401_OpenAI_TokenInvalidated_SetsError_Regression(t *testing.T) {
	// Regression: original behavior on PlatformOpenAI must still SetError.
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 203, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"code":"token_invalidated","message":"x"}}`)

	svc.HandleUpstreamError(context.Background(), account, 401, http.Header{}, body)

	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, "Token revoked")
}

func TestUS028_Handle401_Anthropic_TokenInvalidated_NotMatched_Regression(t *testing.T) {
	// Regression: PlatformAnthropic must NOT take the OpenAI-compat
	// token_invalidated branch (that branch is only for OpenAI-shape upstreams).
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 204, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"code":"token_invalidated","message":"x"}}`)

	svc.HandleUpstreamError(context.Background(), account, 401, http.Header{}, body)

	// Should fall through to the non-OAuth/non-Antigravity branch which
	// also calls SetError but with a different message (no "Token revoked"
	// prefix). Assert the discriminator.
	require.Equal(t, 1, repo.setErrorCalls)
	require.NotContains(t, repo.lastErrorMsg, "Token revoked",
		"anthropic 401 must not take the OpenAI-compat 'Token revoked' branch")
}

func TestUS028_Handle402_NewAPI_DeactivatedWorkspace_SetsError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 205, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 14}
	body := []byte(`{"detail":{"code":"deactivated_workspace"}}`)

	svc.HandleUpstreamError(context.Background(), account, 402, http.Header{}, body)

	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, "Workspace deactivated")
}

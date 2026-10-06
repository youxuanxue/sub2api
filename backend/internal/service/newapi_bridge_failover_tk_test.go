//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTkBridgeUpstreamShouldFailoverAfterPenalty_AccountLevelStatuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  *newapitypes.NewAPIError
	}{
		{"402 insufficient balance", upstreamBridgeError(402, "Insufficient Balance")},
		{"401 auth", upstreamBridgeError(401, "Authentication Fails")},
		{"429 rate limit", upstreamBridgeError(429, "Requests rate limit exceeded")},
		{"arrears 400", arrearsBridgeError(400, dashscopeArrearsMessage, "Arrearage", "Arrearage")},
		{"unpurchased 403", arrearsBridgeError(403, dashscopeUnpurchasedMessage, "AccessDenied.Unpurchased", "AccessDenied.Unpurchased")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.True(t, tkBridgeUpstreamShouldFailoverAfterPenalty(nil, tc.err))
		})
	}
}

func TestTkBridgeUpstreamShouldFailoverAfterPenalty_RequestScopedGatewayOutages(t *testing.T) {
	t.Parallel()
	for _, statusCode := range []int{502, 503, 504} {
		statusCode := statusCode
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			t.Parallel()
			require.True(t, tkBridgeUpstreamShouldFailoverAfterPenalty(nil, upstreamBridgeError(statusCode, "gateway outage")))
		})
	}
}

func TestTkBridgeUpstreamShouldFailoverAfterPenalty_ClientAndOtherServerErrorsNeverMatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  *newapitypes.NewAPIError
	}{
		{"client 400", upstreamBridgeError(400, "The supported API model names are ...")},
		{"generic 403", arrearsBridgeError(403, "Access forbidden by WAF", "access_denied", "AccessDenied")},
		{"model not found 404", upstreamBridgeError(404, "model_not_found")},
		{"server 500", upstreamBridgeError(500, "internal error")},
		{"client forbidden envelope", upstreamBridgeError(400, "Upstream access forbidden, please contact administrator")},
		{"unrelated forbidden", upstreamBridgeError(500, "content access forbidden by policy")},
		{"server 501", upstreamBridgeError(501, "not implemented")},
		{"nil", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.False(t, tkBridgeUpstreamShouldFailoverAfterPenalty(nil, tc.err))
		})
	}
}

func TestTkBridgeUpstreamShouldFailoverAfterPenalty_OpaqueBadResponse404IsNVIDIAOnly(t *testing.T) {
	t.Parallel()
	err := upstreamBridgeError(404, "bad response status code 404")
	require.True(t, tkIsBridgeOpaqueBadResponse404(err))
	require.False(t, tkBridgeUpstreamShouldFailoverAfterPenalty(nil, err),
		"opaque 404 must stay terminal for non-NVIDIA NewAPI (#617 / no pool-wide experiment)")
	require.False(t, tkBridgeUpstreamShouldFailoverAfterPenalty(newNewAPIBridgeAccount(), err),
		"ordinary NewAPI bridge must not inherit the NVIDIA opaque-404 experiment")
	nvidia := &Account{
		ID: 138, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": newapiintegration.NVIDIABuildBaseURL},
	}
	require.True(t, tkBridgeUpstreamShouldFailoverAfterPenalty(nvidia, err),
		"NVIDIA Build opaque 404 must failover to siblings")
	require.False(t, tkIsBridgeOpaqueBadResponse404(upstreamBridgeError(404, "model_not_found")),
		"true model_not_found must stay terminal")
}

func TestTkBridgeUpstreamShouldFailoverAfterPenalty_NVIDIABuildFailoversAllErrors(t *testing.T) {
	t.Parallel()
	nvidia := &Account{
		ID: 138, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": newapiintegration.NVIDIABuildBaseURL},
	}
	require.True(t, isNewAPINVIDIABuildAccount(nvidia))
	for _, tc := range []struct {
		name string
		err  *newapitypes.NewAPIError
	}{
		{"client 400", upstreamBridgeError(400, "The supported API model names are ...")},
		{"model not found 404", upstreamBridgeError(404, "model_not_found")},
		{"opaque 404", upstreamBridgeError(404, "bad response status code 404")},
		{"server 500", upstreamBridgeError(500, "Failed to generate completions")},
		{"server 501", upstreamBridgeError(501, "not implemented")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.True(t, tkBridgeUpstreamShouldFailoverAfterPenalty(nvidia, tc.err),
				"NVIDIA Build must fail over every upstream error to protect UX")
		})
	}
	require.False(t, tkBridgeUpstreamShouldFailoverAfterPenalty(nvidia, nil))
}

func TestTkNewAPIBridgeUpstreamFailoverError_NVIDIAForcesRequestScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	nvidia := &Account{
		ID: 138, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": newapiintegration.NVIDIABuildBaseURL},
	}
	// model_not_found is terminal for ordinary bridge accounts (#617); NVIDIA must
	// still surface a request-scoped failover so siblings absorb the blip.
	err := tkNewAPIBridgeUpstreamFailoverError(c, nvidia, upstreamBridgeError(404, "model_not_found"))
	require.NotNil(t, err)
	require.True(t, err.ShouldRetryNextAccount())
	require.True(t, err.RequestScopedTransient)
	ordinary := newNewAPIBridgeAccount()
	ordinaryErr := tkNewAPIBridgeUpstreamFailoverError(c, ordinary, upstreamBridgeError(404, "model_not_found"))
	require.NotNil(t, ordinaryErr)
	require.True(t, ordinaryErr.ShouldRetryNextAccount())
	require.False(t, ordinaryErr.RequestScopedTransient,
		"non-NVIDIA model_not_found stays account-fault shaped when forced through failover helper")
}

func TestBridgeOpaqueBadResponse404CoolsExecutedModelAndFailovers(t *testing.T) {
	account := parameterCompatibilityAccount(PlatformNewAPI, "z-ai/glm-5.3-flash", protocolrouter.ProtocolChatCompletions)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gpt-5.4", false, []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	snapshot, err := protocolAccountSnapshotForRequest(&account, request)
	require.NoError(t, err)
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	require.NoError(t, err)
	ctx := withProtocolExecutionPlan(context.Background(), plan)
	repo := &modelNotFoundAccountRepoStub{}
	rls := &RateLimitService{accountRepo: repo}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	wrapErr := bridgeWrapRelayErrorAfterPenalty(ctx, rls, c, &account, upstreamBridgeError(404, "bad response status code 404"))
	var failover *UpstreamFailoverError
	require.ErrorAs(t, wrapErr, &failover)
	require.True(t, failover.ShouldRetryNextAccount())
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, plan.ResolvedModel(), repo.modelRateLimitCalls[0].scope)
	require.Equal(t, upstreamOpaqueProvider404Reason, repo.modelRateLimitCalls[0].reason)
	require.WithinDuration(t, time.Now().Add(upstreamOpaqueProvider404Cooldown), repo.modelRateLimitCalls[0].resetAt, 5*time.Second)
	require.Zero(t, repo.tempCalls)
}

func TestBridgeOpaqueBadResponse404DoesNotCoolOrdinaryNewAPI(t *testing.T) {
	ordinary := newNewAPIBridgeAccount()
	repo := &modelNotFoundAccountRepoStub{}
	rls := &RateLimitService{accountRepo: repo}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	wrapErr := bridgeWrapRelayErrorAfterPenalty(context.Background(), rls, c, ordinary, upstreamBridgeError(404, "bad response status code 404"))
	var relay *NewAPIRelayError
	require.ErrorAs(t, wrapErr, &relay, "ordinary NewAPI opaque 404 must stay terminal")
	var failover *UpstreamFailoverError
	require.False(t, errors.As(wrapErr, &failover))
	require.Zero(t, repo.modelRateLimitCalls, "opaque model cool must not expand beyond NVIDIA")
}

func TestBridgeWrapRelayErrorAfterPenalty_GatewayOutageReturnsRequestScopedFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	account := newNewAPIBridgeAccount()
	apiErr := upstreamBridgeError(502, "Network error, please try again later.")

	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), nil, c, account, apiErr)
	require.Error(t, err)

	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, 502, failoverErr.StatusCode)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.Contains(t, string(failoverErr.ResponseBody), "Network error")

	var relayErr *NewAPIRelayError
	require.False(t, errors.As(err, &relayErr))
}

func TestBridgeWrapRelayErrorAfterPenalty_AccountLevelReturnsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	account := newNewAPIBridgeAccount()
	apiErr := upstreamBridgeError(402, "Insufficient Balance")

	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), nil, c, account, apiErr)
	require.Error(t, err)

	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, 402, failoverErr.StatusCode)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.Contains(t, string(failoverErr.ResponseBody), "Insufficient Balance")

	var relayErr *NewAPIRelayError
	require.False(t, errors.As(err, &relayErr))
}

func TestBridgeWrapRelayErrorAfterPenalty_OverloadRetriesWithoutAccountPenalty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), svc, c, newNewAPIBridgeAccount(),
		upstreamBridgeError(529, "Service temporarily overloaded"))
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, 529, failover.StatusCode)
	require.True(t, failover.ShouldRetryNextAccount())
	require.False(t, c.Writer.Written(), "the next account must retain ownership of the response")
	require.False(t, candidateFailureAttributable(err), "overload must not increment the account failure counter")
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setRateLimitedCalls)
	require.Zero(t, repo.tempCalls)
	require.Empty(t, blocker.reasons)
	require.Empty(t, incidents.reasons)
}

func TestBridgeWrapRelayErrorAfterPenalty_ArrearsReturnsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	account := newQwenArrearsAccount()
	apiErr := arrearsBridgeError(400, dashscopeArrearsMessage, "Arrearage", "Arrearage")

	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), nil, c, account, apiErr)
	require.Error(t, err)

	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, 400, failoverErr.StatusCode)
	require.True(t, failoverErr.ShouldRetryNextAccount())
}

func TestBridgeWrapRelayErrorAfterPenalty_UnpurchasedReturnsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc, repo, _, incidents := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()
	account.ID = 129
	apiErr := arrearsBridgeError(http.StatusForbidden, dashscopeUnpurchasedMessage, "AccessDenied.Unpurchased", "AccessDenied.Unpurchased")

	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), svc, c, account, apiErr)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusForbidden, failoverErr.StatusCode)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, []string{tkStandingUnpurchasedIncidentReason}, incidents.reasons)
	var relayErr *NewAPIRelayError
	require.False(t, errors.As(err, &relayErr))
}

func TestBridgeWrapRelayErrorAfterPenalty_Client400ReturnsRelayError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	account := newNewAPIBridgeAccount()
	apiErr := upstreamBridgeError(400, "The supported API model names are ...")

	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), nil, c, account, apiErr)
	require.Error(t, err)

	var relayErr *NewAPIRelayError
	require.True(t, errors.As(err, &relayErr))
	require.Equal(t, 400, relayErr.Err.StatusCode)

	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
}

func TestOpenAIGatewayService_TkWrapBridgeRelayErrorWithPenalty_402Failover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	svc := &OpenAIGatewayService{}
	account := newNewAPIBridgeAccount()

	err := svc.tkWrapBridgeRelayErrorWithPenalty(context.Background(), c, account, upstreamBridgeError(402, "Insufficient Balance"))
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.True(t, failoverErr.ShouldRetryNextAccount())
}

func TestBridgeSupplierUnavailable500RetriesWithoutPenalty(t *testing.T) {
	var mojibake []rune
	for _, b := range []byte("没有可用账号，请稍后重试") {
		mojibake = append(mojibake, rune(b))
	}
	for _, message := range []string{
		"Upstream access forbidden, please contact administrator (request id: example)",
		"没有可用账号，请稍后重试 (request id: example)",
		string(mojibake) + " (request id: example)",
	} {
		t.Run(message, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			svc, repo, blocker, incidents := newBridgePenaltyTestService()
			err := bridgeWrapRelayErrorAfterPenalty(context.Background(), svc, c, newNewAPIBridgeAccount(), upstreamBridgeError(500, message))
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.True(t, failover.ShouldRetryNextAccount())
			require.False(t, candidateFailureAttributable(err))
			require.False(t, c.Writer.Written())
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.setRateLimitedCalls)
			require.Zero(t, repo.tempCalls)
			require.Empty(t, blocker.reasons)
			require.Empty(t, incidents.reasons)
		})
	}
}

func TestBridgeSupplierCapability400RetriesWithoutPenalty(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), svc, c, newNewAPIBridgeAccount(), upstreamBridgeError(400, "[preflight:R3.forced_tool_choice_incompatible] model has always-on thinking"))
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.True(t, failover.ShouldRetryNextAccount())
	require.False(t, candidateFailureAttributable(err))
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.setRateLimitedCalls)
	require.Empty(t, blocker.reasons)
	require.Empty(t, incidents.reasons)
	require.False(t, tkBridgeUpstreamShouldFailoverAfterPenalty(nil, upstreamBridgeError(400, "Thinking may not be enabled when tool_choice forces tool use.")))
}

func TestNativeMessagesSupplierCapability400RetriesWithoutPenalty(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	rateLimit, repo, blocker, incidents := newBridgePenaltyTestService()
	svc := &OpenAIGatewayService{rateLimitService: rateLimit}
	message := "[preflight:R3.forced_tool_choice_incompatible] model has always-on thinking"
	response := &http.Response{StatusCode: 400, Header: make(http.Header)}
	failover := svc.failoverNativeMessagesUpstreamHTTPError(context.Background(), c, newNewAPIBridgeAccount(), response, []byte(`{"error":{"message":"`+message+`"}}`), message, "claude-fable-5")
	require.NotNil(t, failover)
	require.True(t, failover.ShouldRetryNextAccount())
	require.False(t, candidateFailureAttributable(failover))
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setRateLimitedCalls)
	require.Zero(t, repo.tempCalls)
	require.Empty(t, blocker.reasons)
	require.Empty(t, incidents.reasons)
}

func TestBridgeModelRetirementCoolsExecutedModel(t *testing.T) {
	for _, status := range []int{http.StatusGone, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			account := parameterCompatibilityAccount(PlatformNewAPI, "z-ai/glm-5.3-flash", protocolrouter.ProtocolChatCompletions)
			request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gpt-5.4", false, []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`))
			require.NoError(t, err)
			snapshot, err := protocolAccountSnapshotForRequest(&account, request)
			require.NoError(t, err)
			plan, err := NewProtocolRouter().Plan(request, snapshot)
			require.NoError(t, err)
			ctx := withProtocolExecutionPlan(context.Background(), plan)
			repo := &modelNotFoundAccountRepoStub{}
			rls := &RateLimitService{accountRepo: repo}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			err = bridgeWrapRelayErrorAfterPenalty(ctx, rls, c, &account, upstreamBridgeError(status, "The model has reached its end of life and is no longer available"))
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.True(t, failover.ShouldRetryNextAccount())
			require.False(t, c.Writer.Written())
			require.Len(t, repo.modelRateLimitCalls, 1)
			require.Equal(t, plan.ResolvedModel(), repo.modelRateLimitCalls[0].scope)
			require.Equal(t, upstreamModelRetiredReason, repo.modelRateLimitCalls[0].reason)
			require.WithinDuration(t, time.Now().Add(upstreamModelRetiredCooldown), repo.modelRateLimitCalls[0].resetAt, 5*time.Second)
			require.Zero(t, repo.tempCalls)
		})
	}
}

func TestBridgeModelRetirementWithoutPlanDoesNotGuessCooldownKey(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	rls := &RateLimitService{accountRepo: repo}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), rls, c, newNewAPIBridgeAccount(), upstreamBridgeError(http.StatusGone, "The model has been retired"))
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.True(t, failover.ShouldRetryNextAccount())
	require.Empty(t, repo.modelRateLimitCalls)
	require.Zero(t, repo.tempCalls)
}

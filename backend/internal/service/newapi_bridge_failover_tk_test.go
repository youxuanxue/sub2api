//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.True(t, tkBridgeUpstreamShouldFailoverAfterPenalty(tc.err))
		})
	}
}

func TestTkBridgeUpstreamShouldFailoverAfterPenalty_RequestScopedGatewayOutages(t *testing.T) {
	t.Parallel()
	for _, statusCode := range []int{502, 503, 504} {
		statusCode := statusCode
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			t.Parallel()
			require.True(t, tkBridgeUpstreamShouldFailoverAfterPenalty(upstreamBridgeError(statusCode, "gateway outage")))
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
		{"model not found 404", upstreamBridgeError(404, "model_not_found")},
		{"server 500", upstreamBridgeError(500, "internal error")},
		{"client forbidden envelope", upstreamBridgeError(400, "Upstream access forbidden, please contact administrator")},
		{"unrelated forbidden", upstreamBridgeError(500, "content access forbidden by policy")},
		{"server 501", upstreamBridgeError(501, "not implemented")},
		{"nil", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.False(t, tkBridgeUpstreamShouldFailoverAfterPenalty(tc.err))
		})
	}
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
	require.False(t, tkBridgeUpstreamShouldFailoverAfterPenalty(upstreamBridgeError(400, "Thinking may not be enabled when tool_choice forces tool use.")))
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

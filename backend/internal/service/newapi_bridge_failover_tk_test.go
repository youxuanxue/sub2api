//go:build unit

package service

import (
	"context"
	"errors"
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

func TestTkBridgeUpstreamShouldFailoverAfterPenalty_ClientAndOutageNeverMatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  *newapitypes.NewAPIError
	}{
		{"client 400", upstreamBridgeError(400, "The supported API model names are ...")},
		{"model not found 404", upstreamBridgeError(404, "model_not_found")},
		{"server 500", upstreamBridgeError(500, "internal error")},
		{"server 502", upstreamBridgeError(502, "bad gateway")},
		{"nil", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.False(t, tkBridgeUpstreamShouldFailoverAfterPenalty(tc.err))
		})
	}
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
	require.Equal(t, NextAccountRetry, failoverErr.NextAccountAction)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.Contains(t, string(failoverErr.ResponseBody), "Insufficient Balance")

	var relayErr *NewAPIRelayError
	require.False(t, errors.As(err, &relayErr))
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

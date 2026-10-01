//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNewAPIResponsesClientErrorSanitization(t *testing.T) {
	relayErr := &NewAPIRelayError{Err: newapitypes.WithOpenAIError(newapitypes.OpenAIError{
		Message: "Invalid max_output_tokens=8 ?access_token=private-token&refresh_token=private-refresh",
		Type:    "invalid_request_error", Code: "invalid_value", Param: "max_output_tokens",
	}, http.StatusBadRequest)}
	payload, ok := relayErr.ResponsesClientError()
	require.True(t, ok)
	require.Contains(t, payload.Message, "max_output_tokens=8")
	require.NotContains(t, payload.Message, "private-token")
	require.NotContains(t, payload.Message, "private-refresh")
	require.Equal(t, "invalid_request_error", payload.Type)
	require.Equal(t, "invalid_value", payload.Code)
	require.Equal(t, "max_output_tokens", payload.Param)
}

func TestNewAPIResponsesClientErrorRejectsOperationalErrors(t *testing.T) {
	for _, apiErr := range []*newapitypes.NewAPIError{
		errBridgeMissingCredential("api_key"),
		newapitypes.NewErrorWithStatusCode(errors.New("local conversion failed"), newapitypes.ErrorCodeConvertRequestFailed, 400),
		arrearsBridgeError(400, dashscopeArrearsMessage, "Arrearage", "Arrearage"),
		newapitypes.WithOpenAIError(newapitypes.OpenAIError{Message: "invalid key", Code: "channel:invalid_key"}, 400),
		upstreamBridgeError(401, "private credential failure"),
		upstreamBridgeError(402, "balance exhausted"),
		upstreamBridgeError(403, "forbidden"),
		upstreamBridgeError(429, "rate limited"),
		upstreamBridgeError(502, "gateway outage"),
		nil,
	} {
		_, ok := (&NewAPIRelayError{Err: apiErr}).ResponsesClientError()
		require.False(t, ok)
	}
	var relayErr *NewAPIRelayError
	_, ok := relayErr.ResponsesClientError()
	require.False(t, ok)
}

func TestDeepSeekResponsesBudgetErrorDoesNotPenalizeOrFailover(t *testing.T) {
	penalties, repo, blocker, incidents := newBridgePenaltyTestService()
	account := newNewAPIBridgeAccount()
	apiErr := newapitypes.WithOpenAIError(newapitypes.OpenAIError{
		Message: "Invalid 'max_output_tokens': must be greater than or equal to 16, got 8.",
		Param:   "max_output_tokens", Code: "invalid_value", Type: "invalid_request_error",
	}, http.StatusBadRequest)
	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), penalties, &gin.Context{}, account, apiErr)
	var relayErr *NewAPIRelayError
	require.ErrorAs(t, err, &relayErr)
	payload, ok := relayErr.ResponsesClientError()
	require.True(t, ok)
	require.Equal(t, apiErr.ToOpenAIError().Message, payload.Message)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setRateLimitedCalls)
	require.Zero(t, repo.tempCalls)
	require.Empty(t, blocker.reasons)
	require.Empty(t, incidents.reasons)
}

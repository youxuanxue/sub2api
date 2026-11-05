package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSNativeIngressRateLimit_LaterTurnRetriesCurrentTurnNotFirstPacket(t *testing.T) {
	firstPacket := []byte(`{"type":"response.create","model":"gpt-5.6-sol","input":[{"role":"user","content":"first-turn-only"}]}`)
	identity := []byte(`{"type":"response.create","model":"mapped-model","previous_response_id":"resp_old","prompt_cache_key":"client-session","input":[{"type":"function_call_output","call_id":"call_1","output":"current-turn-tool"}]}`)
	fullInput := jsonRawMessages(
		`{"type":"message","role":"user","content":"first-turn-only"}`,
		`{"type":"function_call","call_id":"call_1","name":"inspect","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"call_1","output":"current-turn-tool"}`,
	)
	cause := &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, ResponseBody: []byte(`{"error":{"type":"rate_limit_error"}}`)}

	err := openAIWSNativeIngressRateLimitFailoverError(2, cause, identity, fullInput, true, "gpt-5.6-sol")
	kept := openAIWSIngressRelayResultError(err)
	dialErr := fmt.Errorf("acquire upstream websocket: %w", err)

	for _, candidate := range []error{err, kept, dialErr} {
		retryPayload, retryCurrentTurn := OpenAIWSCurrentTurnRetryPayload(candidate)
		require.True(t, retryCurrentTurn)
		require.NotEmpty(t, retryPayload)
		require.NotEqual(t, string(firstPacket), string(retryPayload))
		require.False(t, gjson.GetBytes(retryPayload, "previous_response_id").Exists())
		require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(retryPayload, "model").String())
		require.Equal(t, "client-session", gjson.GetBytes(retryPayload, "prompt_cache_key").String())
		input := gjson.GetBytes(retryPayload, "input")
		require.True(t, input.IsArray())
		require.Len(t, input.Array(), 3)
		require.Contains(t, input.Raw, "first-turn-only")
		require.Contains(t, input.Raw, "current-turn-tool")
		require.Equal(t, "function_call_output", input.Array()[2].Get("type").String())
		require.Equal(t, "call_1", input.Array()[2].Get("call_id").String())
		require.NotContains(t, string(firstPacket), "current-turn-tool")
	}
}

func TestOpenAIWSNativeIngressRateLimit_UnsafeRebuildDoesNotReplayFirstPacket(t *testing.T) {
	cause := &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}
	identity := []byte(`{"type":"response.create","model":"mapped-model","previous_response_id":"resp_old"}`)
	cases := []struct {
		name      string
		fullInput []json.RawMessage
		exists    bool
	}{
		{
			name: "orphan_tool_output",
			fullInput: []json.RawMessage{
				json.RawMessage(`{"type":"function_call_output","call_id":"missing_call","output":"done"}`),
			},
			exists: true,
		},
		{name: "missing_replay_input"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := openAIWSNativeIngressRateLimitFailoverError(3, cause, identity, tc.fullInput, tc.exists, "gpt-5.6-sol")
			retryPayload, retryCurrentTurn := OpenAIWSCurrentTurnRetryPayload(err)
			require.True(t, retryCurrentTurn)
			require.Empty(t, retryPayload)
			require.NotEqual(t, cause, err)

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Same(t, cause, failoverErr)
			require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
		})
	}
}

func TestOpenAIWSNativeIngressRateLimit_FirstTurnKeepsFirstPacketFailover(t *testing.T) {
	cause := &UpstreamFailoverError{
		StatusCode:   http.StatusTooManyRequests,
		ResponseBody: []byte(`{"error":{"type":"rate_limit_error"}}`),
	}
	fullInput := []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":"current-turn-only"}`),
	}
	identity := []byte(`{"type":"response.create","model":"mapped-model","input":[{"role":"user","content":"current-turn-only"}]}`)

	err := openAIWSNativeIngressRateLimitFailoverError(1, cause, identity, fullInput, true, "gpt-5.6-sol")
	require.True(t, err == error(cause))
	retryPayload, retryCurrentTurn := OpenAIWSCurrentTurnRetryPayload(err)
	require.False(t, retryCurrentTurn)
	require.Nil(t, retryPayload)
	require.Equal(t, err, openAIWSIngressRelayResultError(err))
}

func TestOpenAIWSNativeIngressRateLimit_NonRateLimitStaysPlain(t *testing.T) {
	cause := &UpstreamFailoverError{StatusCode: http.StatusBadGateway}
	err := openAIWSNativeIngressRateLimitFailoverError(4, cause, []byte(`{"type":"response.create"}`), nil, true, "gpt-5.6-sol")
	require.True(t, err == error(cause))
	_, retryCurrentTurn := OpenAIWSCurrentTurnRetryPayload(err)
	require.False(t, retryCurrentTurn)
}

func TestOpenAIWSIngressRelayResultError_UnwrapsIngressTurnAndKeepsCurrentTurn(t *testing.T) {
	writeErr := errors.New("write client websocket event")
	wrapped := wrapOpenAIWSIngressTurnError("write_client", writeErr, true)
	require.ErrorIs(t, openAIWSIngressRelayResultError(wrapped), writeErr)

	cause := &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}
	currentTurn := openAIWSNativeIngressRateLimitFailoverError(
		2,
		cause,
		[]byte(`{"type":"response.create","model":"mapped-model"}`),
		[]json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"current-turn-tool"}`)},
		true,
		"gpt-5.6-sol",
	)
	kept := openAIWSIngressRelayResultError(currentTurn)
	retryPayload, retryCurrentTurn := OpenAIWSCurrentTurnRetryPayload(kept)
	require.True(t, retryCurrentTurn)
	require.Contains(t, string(retryPayload), "current-turn-tool")
	require.NotEqual(t, cause, kept)
}

func jsonRawMessages(items ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(items))
	for i, item := range items {
		out[i] = json.RawMessage(item)
	}
	return out
}

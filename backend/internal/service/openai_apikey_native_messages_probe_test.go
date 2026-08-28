package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeMessagesProtocolProbeRetriesCloudWiseRepresentativeAfterModelRouting401(t *testing.T) {
	account := &Account{
		ID: 95, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{
			"api_key":       "cloudwise-key",
			"base_url":      "https://api.cloudwise.ai/api",
			"model_mapping": modelMappingToAny(openAICloudwiseRelayWildcardModelMappingFloor()),
		},
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		protocolProbeHTTPResponse(http.StatusUnauthorized, `{"error":{"message":"model route unavailable"}}`),
		protocolProbeHTTPResponse(http.StatusOK, `{"type":"message","content":[{"type":"text","text":"OK"}]}`),
	}}
	svc := protocolRequestBuilderTestService(upstream)

	observation, observed := svc.probeOpenAIAPIKeyNativeMessagesSupport(context.Background(), account)

	require.True(t, observed)
	require.Equal(t, ProtocolProbePositive, observation.verdict)
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, openai.DefaultTestModel, gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "MiniMax-M3", gjson.GetBytes(upstream.bodies[1], "model").String())
}

func TestNativeMessagesProtocolProbeDoesNotRetryGenericUnauthorized(t *testing.T) {
	account := &Account{
		ID: 99, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "relay-key",
			"base_url": "https://relay.example/v1",
			"model_mapping": map[string]any{
				"first":  "alpha-model",
				"second": "beta-model",
			},
		},
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		protocolProbeHTTPResponse(http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`),
		protocolProbeHTTPResponse(http.StatusOK, `{"type":"message","content":[{"type":"text","text":"OK"}]}`),
	}}
	svc := protocolRequestBuilderTestService(upstream)

	observation, observed := svc.probeOpenAIAPIKeyNativeMessagesSupport(context.Background(), account)

	require.True(t, observed)
	require.Equal(t, ProtocolProbeInconclusive, observation.verdict)
	require.Len(t, upstream.bodies, 1)
}

func TestNativeMessagesProtocolProbeDoesNotRetryCloudWiseCredentialUnauthorized(t *testing.T) {
	account := &Account{
		ID: 95, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{
			"api_key":       "invalid-cloudwise-key",
			"base_url":      "https://api.cloudwise.ai/api",
			"model_mapping": modelMappingToAny(openAICloudwiseRelayWildcardModelMappingFloor()),
		},
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		protocolProbeHTTPResponse(http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`),
		protocolProbeHTTPResponse(http.StatusOK, `{"type":"message","content":[{"type":"text","text":"OK"}]}`),
	}}
	svc := protocolRequestBuilderTestService(upstream)

	observation, observed := svc.probeOpenAIAPIKeyNativeMessagesSupport(context.Background(), account)

	require.True(t, observed)
	require.Equal(t, ProtocolProbeInconclusive, observation.verdict)
	require.Len(t, upstream.bodies, 1)
}

func TestNativeMessagesProbeSupported(t *testing.T) {
	t.Parallel()
	require.False(t, nativeMessagesProbeSupported(http.StatusNotFound, nil))
	require.False(t, nativeMessagesProbeSupported(http.StatusMethodNotAllowed, nil))
	require.False(t, nativeMessagesProbeSupported(http.StatusBadRequest, []byte(`{"type":"error"}`)))
	require.True(t, nativeMessagesProbeSupported(http.StatusOK, []byte(`{"type":"message","content":[{"type":"text","text":"OK"}]}`)))
	require.True(t, nativeMessagesProbeSupported(http.StatusOK, []byte(`{"content":[{"type":"text","text":"OK"}]}`)))
}

func TestResponsesProbeBodyIndicatesNotImplemented(t *testing.T) {
	t.Parallel()
	require.True(t, responsesProbeBodyIndicatesNotImplemented([]byte(`{"error":{"message":"not implemented","code":"convert_request_failed"}}`)))
	require.False(t, responsesProbeBodyIndicatesNotImplemented([]byte(`{"error":{"message":"rate limit"}}`)))
}

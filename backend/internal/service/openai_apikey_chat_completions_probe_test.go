package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionsProtocolProbeUsesNewAPIAdaptorEndpoint(t *testing.T) {
	account := &Account{
		ID: 60, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeAli, Concurrency: 1,
		Credentials: map[string]any{"api_key": "dashscope-key", "base_url": "https://dashscope.aliyuncs.com", "model_mapping": map[string]any{"qwen3.7-max": "qwen3.7-max"}},
	}
	upstream := &httpUpstreamRecorder{resp: protocolProbeHTTPResponse(http.StatusOK, `{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`)}
	svc := protocolRequestBuilderTestService(upstream)

	observation, observed := svc.probeOpenAIAPIKeyChatCompletionsSupport(context.Background(), account)

	require.True(t, observed)
	require.Equal(t, ProtocolProbePositive, observation.verdict)
	require.Equal(t, protocolrouter.ProtocolChatCompletions, observation.protocol)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions", upstream.lastReq.URL.String())
}

func TestChatCompletionsProtocolProbeClassifiesEndpointNegative(t *testing.T) {
	account := &Account{
		ID: 98, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://compat-upstream.example/v1"},
	}
	upstream := &httpUpstreamRecorder{resp: protocolProbeHTTPResponse(http.StatusNotFound, `{"error":{"message":"not found"}}`)}
	svc := protocolRequestBuilderTestService(upstream)

	observation, observed := svc.probeOpenAIAPIKeyChatCompletionsSupport(context.Background(), account)

	require.True(t, observed)
	require.Equal(t, ProtocolProbeEndpointNegative, observation.verdict)
	require.Equal(t, "https://compat-upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
}

func TestChatCompletionsProtocolProbeRequiresExplicitBaseURL(t *testing.T) {
	account := &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test"}}
	upstream := &httpUpstreamRecorder{}
	svc := protocolRequestBuilderTestService(upstream)

	_, observed := svc.probeOpenAIAPIKeyChatCompletionsSupport(context.Background(), account)

	require.False(t, observed)
	require.Nil(t, upstream.lastReq)
}

func protocolRequestBuilderTestService(upstream *httpUpstreamRecorder) *AccountTestService {
	return &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
}

func protocolProbeHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

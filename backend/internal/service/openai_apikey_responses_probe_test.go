package service

import (
	"context"
	"net/http"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesProtocolProbeRetriesAnotherMappedModelAfterModelSpecificRejection(t *testing.T) {
	account := &Account{
		ID: 7, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeVolcEngine, Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "ark-key",
			"base_url": "https://ark.cn-beijing.volces.com",
			"model_mapping": map[string]any{
				"legacy-lite":      "doubao-1-5-lite-32k-250115",
				"legacy-pro":       "doubao-1-5-pro-32k-250115",
				"legacy-character": "doubao-1-5-pro-32k-character-250715",
				"legacy-vision":    "doubao-1-5-vision-pro-32k-250115",
				"seed":             "doubao-seed-1-6-250615",
			},
		},
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		protocolProbeHTTPResponse(http.StatusForbidden, `{"error":{"message":"The model doubao-1-5-lite-32k-250115 does not support Responses API or you do not have permission to access it"}}`),
		protocolProbeHTTPResponse(http.StatusForbidden, `{"error":{"message":"The model doubao-1-5-pro-32k-250115 does not support Responses API"}}`),
		protocolProbeHTTPResponse(http.StatusForbidden, `{"error":{"message":"The model doubao-1-5-pro-32k-character-250715 does not support Responses API"}}`),
		protocolProbeHTTPResponse(http.StatusForbidden, `{"error":{"message":"The model doubao-1-5-vision-pro-32k-250115 does not support Responses API"}}`),
		protocolProbeHTTPResponse(http.StatusOK, `{"status":"completed","output":[{"type":"function_call","name":"probe_ping"}]}`),
	}}
	svc := protocolRequestBuilderTestService(upstream)

	observation, observed := svc.probeOpenAIAPIKeyResponsesSupport(context.Background(), account)

	require.True(t, observed)
	require.Equal(t, ProtocolProbePositive, observation.verdict)
	require.Len(t, upstream.bodies, 5)
	require.Equal(t, "doubao-1-5-lite-32k-250115", gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "doubao-seed-1-6-250615", gjson.GetBytes(upstream.bodies[4], "model").String())
}

func TestResponsesProtocolProbeDoesNotFanOutTransientUpstreamFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "rate limit", status: http.StatusTooManyRequests, body: `{"error":{"message":"model is unavailable due to quota"}}`},
		{name: "server failure", status: http.StatusServiceUnavailable, body: `{"error":{"message":"No available accounts"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				ID: 176, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
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
				protocolProbeHTTPResponse(tc.status, tc.body),
				protocolProbeHTTPResponse(http.StatusOK, `{"status":"completed","output":[{"type":"function_call","name":"probe_ping"}]}`),
			}}
			svc := protocolRequestBuilderTestService(upstream)

			observation, observed := svc.probeOpenAIAPIKeyResponsesSupport(context.Background(), account)

			require.True(t, observed)
			require.Equal(t, ProtocolProbeInconclusive, observation.verdict)
			require.Len(t, upstream.bodies, 1)
		})
	}
}

func TestResponsesProtocolProbeDoesNotFanOutProjectAuthorizationFailure(t *testing.T) {
	account := &Account{
		ID: 107, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "project-key",
			"base_url": "https://relay.example/v1",
			"model_mapping": map[string]any{
				"first":  "alpha-model",
				"second": "beta-model",
			},
		},
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		protocolProbeHTTPResponse(http.StatusForbidden, `{"error":{"message":"You do not have permission to invoke any model in this project"}}`),
		protocolProbeHTTPResponse(http.StatusOK, `{"status":"completed","output":[{"type":"function_call","name":"probe_ping"}]}`),
	}}
	svc := protocolRequestBuilderTestService(upstream)

	observation, observed := svc.probeOpenAIAPIKeyResponsesSupport(context.Background(), account)

	require.True(t, observed)
	require.Equal(t, ProtocolProbeInconclusive, observation.verdict)
	require.Len(t, upstream.bodies, 1)
}

func TestDecideResponsesProbeSupport(t *testing.T) {
	fnCall := []byte(`{"output":[{"type":"reasoning"},{"type":"function_call","name":"probe_ping"}]}`)
	reasoningOnly := []byte(`{"output":[{"type":"reasoning"}]}`)

	cases := []struct {
		name   string
		status int
		body   []byte
		want   bool
	}{
		// Endpoint clearly absent on third-party OpenAI-compatible upstreams.
		{"404 endpoint absent", 404, fnCall, false},
		{"405 method not allowed", 405, fnCall, false},
		// 2xx: tool capability is judged by presence of a function_call output item.
		{"200 with function_call", 200, fnCall, true},
		// Volcengine Ark coding/v3 × kimi-k2.6: reasoning only, no function_call.
		{"200 reasoning only", 200, reasoningOnly, false},
		{"200 invalid json", 200, []byte("not-json"), false},
		{"200 no output field", 200, []byte(`{"status":"completed"}`), false},
		// Non-2xx (other than 404/405): endpoint exists, capability undecidable -> conservative true.
		{"400 conservative true", 400, reasoningOnly, true},
		{"401 conservative true", 401, nil, true},
		{"500 conservative true", 500, nil, true},
		{"500 not implemented", 500, []byte(`{"error":{"message":"not implemented","code":"convert_request_failed"}}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpointSupported := openai_compat.ResponsesEndpointSupportedByStatus(tc.status)
			require.Equal(t, tc.want, decideResponsesProbeSupport(endpointSupported, tc.status, tc.body))
		})
	}
}

func TestResponsesProbeVerdictIsConclusive(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"200_completed", 200, `{"status":"completed","output":[]}`, true},
		{"200_incomplete_max_output_tokens", 200, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, false},
		{"200_incomplete_content_filter", 200, `{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}`, true},
		{"200_failed", 200, `{"status":"failed"}`, false},
		{"200_no_status_field", 200, `{"output":[]}`, true},
		{"404_ignores_body_status", 404, `{"status":"failed"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, responsesProbeVerdictIsConclusive(tc.status, []byte(tc.body)))
		})
	}
}

func TestResponsesProbeBodyHasFunctionCall(t *testing.T) {
	require.True(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[{"type":"function_call"}]}`)))
	require.True(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[{"type":"reasoning"},{"type":"function_call"}]}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[{"type":"reasoning"}]}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`{"output":[]}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`{}`)))
	require.False(t, responsesProbeBodyHasFunctionCall([]byte(`garbage`)))
}

func TestSelectProtocolProbeModelUsesDeterministicTextFallback(t *testing.T) {
	// No model_mapping -> fall back to DefaultTestModel (OpenAI official APIKey).
	require.Equal(t, openai.DefaultTestModel, selectProtocolProbeModel(&Account{}))

	// model_mapping values are upstream models; pick first by sort for reproducibility.
	acct := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{
			"client-b": "zeta-model",
			"client-a": "alpha-model",
		},
	}}
	require.Equal(t, "alpha-model", selectProtocolProbeModel(acct))

	// Wildcard / blank upstream values are skipped.
	acctWild := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{
			"a": "*",
			"b": "  ",
			"c": "real-model",
		},
	}}
	require.Equal(t, "real-model", selectProtocolProbeModel(acctWild))

	// Only wildcard mappings -> DefaultTestModel.
	acctAllWild := &Account{Credentials: map[string]any{
		"model_mapping": map[string]any{"a": "gpt-*"},
	}}
	require.Equal(t, openai.DefaultTestModel, selectProtocolProbeModel(acctAllWild))
}

func TestSelectProtocolProbeModelExcludesMediaAndUsesDeterministicTextFallback(t *testing.T) {
	account := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"video": "doubao-seedance-2-0-260128",
				"later": "qwen-plus",
				"first": "glm-4.5",
				"image": "doubao-seedream-4-0-250828",
			},
		},
	}

	require.Equal(t, "glm-4.5", selectProtocolProbeModel(account))
}

func TestSelectProtocolProbeModelPrefersAliNativeQwenFamily(t *testing.T) {
	account := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"glm":  "glm-4.5",
				"qwen": "qwen-plus",
			},
		},
	}

	require.Equal(t, "qwen-plus", selectProtocolProbeModel(account))
}

func TestSelectProtocolProbeModelSkipsEmbeddingModels(t *testing.T) {
	account := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"embedding": "bge-large-en",
				"chat":      "ernie-4.5-turbo-32k",
			},
		},
	}

	require.Equal(t, "ernie-4.5-turbo-32k", selectProtocolProbeModel(account))
}

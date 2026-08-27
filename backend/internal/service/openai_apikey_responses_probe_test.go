package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
)

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

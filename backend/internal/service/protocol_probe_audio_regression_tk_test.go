//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProtocolProbeExcludesSpeechModes(t *testing.T) {
	for _, mode := range []string{"tts", "audio_speech", "audio_transcription"} {
		t.Run(mode, func(t *testing.T) {
			tkOverlayMu.Lock()
			previous := tkOverlayEffective
			tkOverlayEffective = &tkPricingOverlaySnapshot{Models: map[string]*LiteLLMModelPricing{
				"opaque-supply": {Mode: mode}, "text-supply": {Mode: "chat"},
			}}
			tkOverlayMu.Unlock()
			t.Cleanup(func() { tkOverlayMu.Lock(); tkOverlayEffective = previous; tkOverlayMu.Unlock() })
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "test", "base_url": "https://example.test",
					"model_mapping": map[string]any{"speech": "opaque-supply", "text": "text-supply"}}}
			require.Equal(t, []string{"text-supply"}, protocolProbeModelCandidates(account))
			account.Credentials["model_mapping"] = map[string]any{"speech": "opaque-supply"}
			require.Empty(t, ProtocolProbeCandidates(account), "speech-only accounts must not issue text probes")
		})
	}
}

func TestAliProtocolProbeUsesTextModelAndRestoresThinkingChatRoute(t *testing.T) {
	account := Account{ID: 24, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli, Concurrency: 1, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "test-only", "base_url": newapiintegration.AliTokenPlanBaseURL}}
	// Use the production manifest, including its speech entry, rather than a second model list.
	mapping, ok := accountModelMappingForAccount(context.Background(), &account, nil, nil, nil)
	require.True(t, ok)
	rawMapping := make(map[string]any, len(mapping))
	for name, target := range mapping {
		rawMapping[name] = target
	}
	account.Credentials["model_mapping"] = rawMapping
	upstream := &httpUpstreamRecorder{resp: protocolProbeHTTPResponse(http.StatusOK, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)}
	observation, observed := protocolRequestBuilderTestService(upstream).probeOpenAIAPIKeyChatCompletionsSupport(context.Background(), &account)
	require.True(t, observed)
	require.Equal(t, ProtocolProbePositive, observation.verdict)
	require.Equal(t, newapiintegration.AliTokenPlanBaseURL+"/compatible-mode/v1/chat/completions", upstream.lastReq.URL.String())
	model := gjson.GetBytes(upstream.bodies[0], "model").String()
	require.Contains(t, mapping, model)
	require.NotEqual(t, "qwen-audio-3.0-tts-plus", model, "incident: speech model was used as Chat witness")
	require.False(t, protocolProbeModelIsNonText(model))
	protocols, err := ApplyProtocolProbeVerdicts([]protocolrouter.Protocol{protocolrouter.ProtocolResponses}, map[protocolrouter.Protocol]ProtocolProbeVerdict{observation.protocol: observation.verdict})
	require.NoError(t, err)
	attachTestProtocolCapability(&account, protocols...)
	body := []byte(`{"model":"qwen3.8-flash","messages":[{"role":"user","content":"OK"}],"thinking":{"type":"disabled"},"max_tokens":128}`)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "qwen3.8-flash", false, body)
	require.NoError(t, err)
	snapshot, err := protocolAccountSnapshotForRequest(&account, request)
	require.NoError(t, err)
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	require.NoError(t, err)
	require.Equal(t, protocolrouter.ProtocolChatCompletions, plan.TargetProtocol())
	require.Equal(t, request.Digest(), plan.EffectiveRequestDigest(), "explicit thinking-off must remain intact")
}

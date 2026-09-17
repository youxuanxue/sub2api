//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

// Regression: Gemini /v1beta glm-5.3 with a Chat-capable NewAPI peer must not
// become a platform routing 500 when an unrelated Antigravity peer has invalid
// capability evidence and does not map the model.
func TestCandidateEligibilityGeminiGLMChatIgnoresIrrelevantCapabilityUnknown(t *testing.T) {
	chatOnly := *protocolRoutingOpenAIAccount(115, "chat_completions")
	chatOnly.Platform = PlatformNewAPI
	chatOnly.ChannelType = 1
	chatOnly.GroupIDs = []int64{284}
	chatOnly.Credentials["model_mapping"] = map[string]any{"glm-5.3": "glm-5.3"}
	attachTestProtocolCapability(&chatOnly, protocolrouter.ProtocolChatCompletions)

	invalid := Account{
		ID: 62, GroupIDs: []int64{21}, Platform: PlatformAntigravity,
		Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10,
		Credentials: map[string]any{
			"base_url": "https://api-us4.tokenkey.dev", "api_key": "test-only",
			"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"},
		},
	}
	attachTestProtocolCapability(&invalid)
	invalid.ProtocolEndpointCapability.ProbeEvidence = ProtocolProbeEvidence{}

	groups := []Group{grp(284, PlatformNewAPI, 1, false), grp(21, PlatformAntigravity, 2, false)}
	r, _, key := globalCandidateFixture(groups, []Account{chatOnly, invalid})

	const model = "glm-5.3"
	path := "/v1beta/models/" + model + ":streamGenerateContent"
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"Reply with OK"}]}]}`)
	ctx := r.WithRequest(context.Background(), ShapeGemini, path, model, body)

	ok, err := r.candidateGateway.candidateSupportsRequest(ctx, &invalid, PlatformAntigravity, false, model, ShapeGemini)
	require.False(t, ok)
	require.ErrorIs(t, err, ErrUniversalUnsupportedModel)
	require.NotErrorIs(t, err, ErrProtocolCapabilityUnknown)

	plan, governed, planErr := protocolPlanForAccount(ctx, &chatOnly, model)
	require.True(t, governed)
	require.NoError(t, planErr)
	require.Equal(t, protocolrouter.AdapterGeminiToChat, plan.AdapterID())

	_, state, err := r.PrepareCandidateRequest(ctx, key, ShapeGemini, path, model, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, state)
	require.NotNil(t, state.current)
	require.Equal(t, int64(115), state.current.account.ID)
	require.NotNil(t, state.current.plan)
	require.Equal(t, protocolrouter.AdapterGeminiToChat, state.current.plan.AdapterID())
}

func TestCandidateEligibilityGeminiGLMOnlyIrrelevantCapabilityUnknownIsUnsupportedModel(t *testing.T) {
	invalid := Account{
		ID: 62, GroupIDs: []int64{21}, Platform: PlatformAntigravity,
		Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10,
		Credentials: map[string]any{
			"base_url": "https://api-us4.tokenkey.dev", "api_key": "test-only",
			"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"},
		},
	}
	attachTestProtocolCapability(&invalid)
	invalid.ProtocolEndpointCapability.ProbeEvidence = ProtocolProbeEvidence{}

	r, _, key := globalCandidateFixture(
		[]Group{grp(21, PlatformAntigravity, 2, false)},
		[]Account{invalid},
	)

	const model = "glm-5.3"
	path := "/v1beta/models/" + model + ":streamGenerateContent"
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"Reply with OK"}]}]}`)
	ctx := r.WithRequest(context.Background(), ShapeGemini, path, model, body)
	_, _, err := r.PrepareCandidateRequest(ctx, key, ShapeGemini, path, model, body, "", "")
	require.ErrorIs(t, err, ErrUniversalUnsupportedModel)
	require.NotErrorIs(t, err, ErrProtocolCapabilityUnknown)
}

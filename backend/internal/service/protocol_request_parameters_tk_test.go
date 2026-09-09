//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func parameterCompatibilityAccount(platform, upstreamModel string, protocol protocolrouter.Protocol) Account {
	account := globalCandidateAccount(101, 1, 10)
	account.Platform = platform
	account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": upstreamModel}
	if platform == PlatformNewAPI {
		account.ChannelType = newapiconstant.ChannelTypeOpenAI
		account.Credentials["base_url"] = newapiintegration.NVIDIABuildBaseURL
	}
	attachTestProtocolCapability(&account, protocol)
	return account
}

func TestProtocolPlanPreservesExplicitParameters(t *testing.T) {
	for _, tc := range []struct {
		name, platform, upstream, fields string
		protocol                         protocolrouter.Protocol
		blocked                          bool
	}{
		{"nvidia-thinking-on", PlatformNewAPI, "deepseek-ai/deepseek-v4-pro-0813", `,"enable_thinking":true`, protocolrouter.ProtocolChatCompletions, true},
		{"nvidia-thinking-off", PlatformNewAPI, "deepseek-ai/deepseek-v4-pro-0813", `,"enable_thinking":false`, protocolrouter.ProtocolChatCompletions, true},
		{"nvidia-default", PlatformNewAPI, "deepseek-ai/deepseek-v4-pro-0813", "", protocolrouter.ProtocolChatCompletions, false},
		{"other-nvidia-model", PlatformNewAPI, "moonshotai/kimi-k3", `,"enable_thinking":true`, protocolrouter.ProtocolChatCompletions, false},
		{"other-provider", PlatformOpenAI, "deepseek-ai/deepseek-v4-pro-0813", `,"enable_thinking":true`, protocolrouter.ProtocolChatCompletions, false},
		{"grok-chat-none", PlatformGrok, "grok-4.6", `,"reasoning_effort":"none"`, protocolrouter.ProtocolChatCompletions, true},
		{"grok-chat-camel", PlatformGrok, "grok-4.6", `,"reasoningEffort":"none"`, protocolrouter.ProtocolChatCompletions, true},
		{"grok-latest-none", PlatformGrok, "grok-4.6-latest", `,"reasoning_effort":"none"`, protocolrouter.ProtocolChatCompletions, true},
		{"grok-responses-none", PlatformGrok, "grok-4.6", `,"reasoning":{"effort":"none"}`, protocolrouter.ProtocolResponses, true},
		{"grok-responses-flat-none", PlatformGrok, "grok-4.6", `,"reasoning_effort":"none"`, protocolrouter.ProtocolResponses, true},
		{"grok-chat-high", PlatformGrok, "grok-4.6", `,"reasoning_effort":"high"`, protocolrouter.ProtocolChatCompletions, false},
		{"grok-default", PlatformGrok, "grok-4.6", "", protocolrouter.ProtocolChatCompletions, false},
		{"other-grok-model", PlatformGrok, "grok-3-mini", `,"reasoning_effort":"none"`, protocolrouter.ProtocolChatCompletions, false},
		{"openai-none", PlatformOpenAI, "gpt-5.4", `,"reasoning_effort":"none"`, protocolrouter.ProtocolChatCompletions, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := parameterCompatibilityAccount(tc.platform, tc.upstream, tc.protocol)
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"input":"hi"%s}`, tc.fields))
			request, err := protocolrouter.ParseCanonicalRequest(tc.protocol, protocolrouter.ResponsesPathNone, "gpt-5.4", false, body)
			require.NoError(t, err)
			snapshot, err := protocolAccountSnapshotForRequest(&account, request)
			require.NoError(t, err)
			_, err = NewProtocolRouter().Plan(request, snapshot)
			if tc.blocked {
				require.ErrorIs(t, err, protocolrouter.ErrNoLegalRoute)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, body, request.Body(), "eligibility must not rewrite client intent")
			require.Equal(t, []protocolrouter.Protocol{tc.protocol}, account.ProtocolEndpointCapability.SupportedProtocols)
			metadata, err := ProtocolAccountSnapshot(&account, "gpt-5.4")
			require.NoError(t, err)
			_, err = NewProtocolRouter().Plan(request, metadata)
			require.NoError(t, err, "metadata has no explicit parameter constraint")
		})
	}
}

func TestGlobalCandidateSkipsParameterIncompatibleProvider(t *testing.T) {
	for _, tc := range []struct{ name, platform, upstream, fields string }{
		{"nvidia-thinking", PlatformNewAPI, "deepseek-ai/deepseek-v4-pro-0813", `,"enable_thinking":true`},
		{"grok-none", PlatformGrok, "grok-4.6", `,"reasoning_effort":"none"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			incompatible := parameterCompatibilityAccount(tc.platform, tc.upstream, protocolrouter.ProtocolChatCompletions)
			peer := globalCandidateAccount(202, 20, 10)
			r, _, key := globalCandidateFixture([]Group{grpNoImage(10, PlatformOpenAI, 0, false)}, []Account{incompatible, peer})
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]%s}`, tc.fields))
			ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, "", "")
			require.NoError(t, err)
			require.Equal(t, peer.ID, state.current.account.ID)
			selection, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", nil, "", key.UserID)
			require.NoError(t, err)
			require.Equal(t, peer.ID, selection.Account.ID)
			require.Equal(t, peer.ID, selection.ProtocolPlan.AccountID())
			selection.ReleaseFunc()
			require.Equal(t, body, state.body)

			r, _, key = globalCandidateFixture([]Group{grpNoImage(10, PlatformOpenAI, 0, false)}, []Account{incompatible})
			_, _, err = r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, "", "")
			require.Error(t, err, "no legal peer must fail before transport")
		})
	}
}

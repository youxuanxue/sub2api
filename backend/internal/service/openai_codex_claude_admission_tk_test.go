//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestCodexClaudeModelAdmission(t *testing.T) {
	for _, kind := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey, "edge"} {
		for _, tc := range []struct {
			name, requested, mapped        string
			wildcard, passthrough, allowed bool
		}{
			{name: "empty mapping", requested: "claude-fable-5-1"},
			{name: "sonnet", requested: "claude-sonnet-4-6"},
			{name: "namespaced opus", requested: "anthropic/CLAUDE-opus-5-5"},
			{name: "stale identity", requested: "claude-sonnet-5", mapped: "claude-sonnet-5"},
			{name: "alias into claude", requested: "client-alias", mapped: "claude-fable-5-1"},
			{name: "wildcard into claude", requested: "client-alias", mapped: "claude-sonnet-4-6", wildcard: true},
			{name: "passthrough", requested: "claude-fable-5-1", passthrough: true},
			{name: "passthrough ignores remap", requested: "claude-sonnet-5", mapped: "gpt-6-sol", passthrough: true},
			{name: "explicit supported remap", requested: "claude-sonnet-5", mapped: "gpt-6-sol", allowed: true},
			{name: "supported model", requested: "gpt-6-sol", allowed: true},
			{name: "unrelated alias", requested: "custom-model", allowed: true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				a := &Account{ID: 1, Platform: PlatformOpenAI, Type: kind, Credentials: map[string]any{}, Extra: map[string]any{}}
				if kind == "edge" {
					a.Type = AccountTypeAPIKey
					a.Credentials["base_url"] = "https://api-uk1.tokenkey.dev"
				}
				if tc.mapped != "" {
					key := tc.requested
					if tc.wildcard {
						key = "*"
					}
					a.Credentials["model_mapping"] = map[string]any{key: tc.mapped}
				}
				a.Extra["openai_passthrough"] = tc.passthrough
				want := tc.allowed || kind == AccountTypeAPIKey
				require.Equal(t, want, a.IsModelSupported(tc.requested))
				// An unmapped generic passthrough already has its own scheduler
				// whitelist; this fix must not broaden that existing policy.
				admitted := want && !(kind == AccountTypeAPIKey && tc.passthrough && tc.mapped == "")
				require.Equal(t, admitted, accountAdmitsRequestedModel(a, tc.requested, nil))
			})
		}
	}
}

func TestCodexClaudePlanAndSelection(t *testing.T) {
	const model = "claude-sonnet-4-6"
	oauth := *officialOpenAIOAuthAccount(11)
	oauth.GroupIDs, oauth.Concurrency, oauth.Priority = []int64{10}, 10, 1
	edge := multimodalSupplyOpenAIEdgeAccount(12, 10)
	edge.Credentials["model_mapping"] = map[string]any{model: model}
	api := multimodalSupplyTokenseaAccount(13, 10)
	api.Credentials["model_mapping"] = map[string]any{model: model}
	api.Priority = 100
	for _, protocol := range []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses, protocolrouter.ProtocolMessages} {
		t.Run(string(protocol), func(t *testing.T) {
			req, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{InboundProtocol: protocol, RequestedModel: model, Profile: protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText}, Body: multimodalTextChatBody(model)})
			require.NoError(t, err)
			ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), req)
			for _, a := range []*Account{&oauth, &edge} {
				_, governed, err := protocolPlanForAccount(ctx, a, model)
				require.True(t, governed)
				require.ErrorIs(t, err, protocolrouter.ErrModelPolicyDenied)
			}
			plan, governed, err := protocolPlanForAccount(ctx, &api, model)
			require.NoError(t, err)
			require.True(t, governed)
			require.Equal(t, model, plan.ResolvedModel())
		})
	}
	groups := []Group{grp(10, PlatformOpenAI, 1, false)}
	// Direct groups may deliberately translate Claude names to GPT. Keep this
	// request on the actual Claude wire model to exercise the supply negative.
	groups[0].MessagesDispatchModelConfig.ExactModelMappings = map[string]string{model: model}
	for _, mode := range []string{RoutingModeDirect, RoutingModeUniversal} {
		for _, includeAPI := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/codex-only", true: "/valid-supplier"}[includeAPI], func(t *testing.T) {
				accounts := []Account{oauth, edge}
				if includeAPI {
					accounts = append(accounts, api)
				}
				r, _, key := globalCandidateFixture(groups, accounts)
				key.RoutingMode = mode
				if mode == RoutingModeDirect {
					key.Group, key.GroupID = &groups[0], &groups[0].ID
				}
				_, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", model, multimodalTextChatBody(model), "", "")
				if includeAPI {
					require.NoError(t, err)
					require.Equal(t, api.ID, state.current.account.ID)
				} else {
					require.ErrorIs(t, err, ErrUniversalUnsupportedModel)
					require.Nil(t, state)
				}
			})
		}
	}
}

func TestCodexClaudeCompactMapping(t *testing.T) {
	a := officialOpenAIOAuthAccount(21)
	a.Credentials["compact_model_mapping"] = map[string]any{"gpt-6-sol": "claude-fable-5-1"}
	req, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{InboundProtocol: protocolrouter.ProtocolResponses, ResponsesPath: protocolrouter.ResponsesPathCompact, RequestedModel: "gpt-6-sol", Profile: protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText}, Body: []byte(`{"model":"gpt-6-sol","input":"hello"}`)})
	require.NoError(t, err)
	ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), req)
	_, _, err = protocolPlanForAccount(ctx, a, "gpt-6-sol")
	require.ErrorIs(t, err, protocolrouter.ErrModelPolicyDenied)
}

func TestCodexClaudeAliasMappedToGPTPlan(t *testing.T) {
	const alias = "claude-sonnet-5"
	a := officialOpenAIOAuthAccount(31)
	a.Credentials["model_mapping"] = map[string]any{alias: "gpt-6-sol"}
	for _, protocol := range []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses} {
		t.Run(string(protocol), func(t *testing.T) {
			req, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{InboundProtocol: protocol, RequestedModel: alias, Profile: protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText}, Body: multimodalTextChatBody(alias)})
			require.NoError(t, err)
			ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), req)
			plan, governed, err := protocolPlanForAccount(ctx, a, alias)
			require.NoError(t, err)
			require.True(t, governed)
			require.Equal(t, "gpt-6-sol", plan.ResolvedModel())
			require.Equal(t, protocolrouter.ProtocolResponses, plan.TargetProtocol())
		})
	}
}

func TestCodexClaudeCatalogProjection(t *testing.T) {
	const model = "claude-fable-5-1"
	for _, kind := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey, "edge"} {
		t.Run(kind, func(t *testing.T) {
			a := &Account{Platform: PlatformOpenAI, Type: kind, Credentials: map[string]any{}}
			if kind == "edge" {
				a.Type = AccountTypeAPIKey
				a.Credentials["base_url"] = "https://api-uk1.tokenkey.dev"
			}
			for _, codex := range []bool{false, true} {
				body := []byte(`{"data":[{"id":"claude-fable-5-1"},{"id":"gpt-6-sol"}]}`)
				if codex {
					body = []byte(`{"models":[{"slug":"claude-fable-5-1"},{"slug":"gpt-6-sol"}]}`)
				}
				out, err := projectAccountModelsBody(body, a, nil, codex)
				require.NoError(t, err)
				require.Contains(t, string(out), "gpt-6-sol")
				if kind == AccountTypeAPIKey {
					require.Contains(t, string(out), model)
				} else {
					require.NotContains(t, string(out), model)
				}
			}
		})
	}
}

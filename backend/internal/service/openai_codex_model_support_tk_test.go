//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestCodexRetiredModelAdmission(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey, "edge"} {
		for _, tc := range []struct {
			name, requested, mapped string
			wildcard                bool
			passthrough, allowed    bool
		}{
			{name: "empty mapping", requested: "gpt-5.5"},
			{name: "stale identity", requested: "gpt-5.5", mapped: "gpt-5.5"},
			{name: "wildcard into retired", requested: "client-alias", mapped: "gpt-5.5", wildcard: true},
			{name: "alias into retired", requested: "client-alias", mapped: "gpt-5.5"},
			{name: "spelling and effort", requested: "openai/GPT5.5-high"},
			{name: "dated model", requested: "gpt-5.5-2026-04-23"},
			{name: "legacy wire alias", requested: "gpt-5"},
			{name: "compat alias", requested: "gpt-5.5-pro"},
			{name: "passthrough", requested: "gpt-5.5", passthrough: true},
			{name: "passthrough ignores remap", requested: "gpt-5.5", mapped: "gpt-6-sol", passthrough: true},
			{name: "explicit supported remap", requested: "gpt-5.5", mapped: "gpt-6-sol", allowed: true},
			{name: "supported model", requested: "gpt-6-sol", allowed: true},
			{name: "unrelated custom alias", requested: "custom-model", allowed: true},
		} {
			t.Run(accountType+"/"+tc.name, func(t *testing.T) {
				a := &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{}, Extra: map[string]any{}}
				if accountType == "edge" {
					a.Type = AccountTypeAPIKey
					a.Credentials["base_url"] = "https://api-us4.tokenkey.dev"
				}
				if tc.mapped != "" {
					key := tc.requested
					if tc.wildcard {
						key = "*"
					}
					a.Credentials["model_mapping"] = map[string]any{key: tc.mapped}
				}
				if tc.passthrough {
					a.Extra["openai_passthrough"] = true
				}
				want := tc.allowed || accountType == AccountTypeAPIKey
				require.Equal(t, want, a.IsModelSupported(tc.requested))
				require.Equal(t, want, accountAdmitsRequestedModel(a, tc.requested, nil))
			})
		}
	}
}

func TestCodexRetiredModelPlanAndSelection(t *testing.T) {
	oauth := *officialOpenAIOAuthAccount(11)
	oauth.GroupIDs, oauth.Concurrency, oauth.Priority = []int64{10}, 10, 1
	oauth.Credentials["model_mapping"] = map[string]any{"gpt-5.5": "gpt-5.5"}
	edge := multimodalSupplyOpenAIEdgeAccount(12, 10)
	api := multimodalSupplyTokenseaAccount(13, 10)
	api.Priority = 100 // OAuth and the edge would win without the capability gate.

	for _, protocol := range []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses, protocolrouter.ProtocolMessages} {
		t.Run(string(protocol), func(t *testing.T) {
			req, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
				InboundProtocol: protocol, RequestedModel: "gpt-5.5",
				Profile: protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
				Body:    []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`),
			})
			require.NoError(t, err)
			ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), req)
			for _, rejected := range []*Account{&oauth, &edge} {
				_, governed, err := protocolPlanForAccount(ctx, rejected, "gpt-5.5")
				require.True(t, governed)
				require.ErrorIs(t, err, protocolrouter.ErrModelPolicyDenied)
			}
			plan, governed, err := protocolPlanForAccount(ctx, &api, "gpt-5.5")
			require.NoError(t, err)
			require.True(t, governed)
			require.Equal(t, "gpt-5.5", plan.ResolvedModel())
		})
	}

	groups := []Group{grp(10, PlatformOpenAI, 1, false)}
	for _, mode := range []string{RoutingModeDirect, RoutingModeUniversal} {
		t.Run(mode, func(t *testing.T) {
			for _, includeAPI := range []bool{true, false} {
				accounts := []Account{oauth, edge}
				if includeAPI {
					accounts = append(accounts, api)
				}
				r, _, key := globalCandidateFixture(groups, accounts)
				key.RoutingMode = mode
				if mode == RoutingModeDirect {
					key.Group, key.GroupID = &groups[0], &groups[0].ID
				}
				_, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat,
					"/v1/chat/completions", "gpt-5.5", multimodalTextChatBody("gpt-5.5"), "", "")
				if includeAPI {
					require.NoError(t, err)
					require.Equal(t, api.ID, state.current.account.ID)
				} else {
					require.ErrorIs(t, err, ErrUniversalUnsupportedModel, "retired-only pool is unsupported, not capacity 429")
					require.Nil(t, state)
				}

				svc, discoveryKey := candidateDiscoveryFixture(groups, accounts)
				discoveryKey.RoutingMode = mode
				if mode == RoutingModeDirect {
					discoveryKey.Group, discoveryKey.GroupID = &groups[0], &groups[0].ID
				}
				models, err := svc.List(context.Background(), discoveryKey, UniversalProtocolOpenAI)
				require.NoError(t, err)
				found := false
				for _, model := range models {
					found = found || model.ID == "gpt-5.5"
				}
				require.Equal(t, includeAPI, found, "discovery must preserve GPT-5.5 only through a valid supply")
			}
		})
	}

}

func TestCodexRetiredModelCompactMapping(t *testing.T) {
	a := officialOpenAIOAuthAccount(21)
	a.Credentials["compact_model_mapping"] = map[string]any{"gpt-6-sol": "gpt-5.5"}
	req, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolResponses, ResponsesPath: protocolrouter.ResponsesPathCompact,
		RequestedModel: "gpt-6-sol", Profile: protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
		Body: []byte(`{"model":"gpt-6-sol","input":"hello"}`),
	})
	require.NoError(t, err)
	ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), req)
	_, _, err = protocolPlanForAccount(ctx, a, "gpt-6-sol")
	require.ErrorIs(t, err, protocolrouter.ErrModelPolicyDenied, "compact mapping cannot reintroduce a retired Codex wire model")
}

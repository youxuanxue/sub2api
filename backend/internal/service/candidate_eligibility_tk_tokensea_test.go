//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestCandidateEligibilityTokenseaUsesPlanAcrossEntrances(t *testing.T) {
	t.Run("typed_url_rejects_gpt", func(t *testing.T) {
		const model = "gpt-5.6-luna"
		account := tokenseaAnthropicRelayAccount()
		account.ID, account.GroupIDs = 93, []int64{1}
		account.Status, account.Schedulable = StatusActive, true
		account.Credentials["api_key"] = "test-only"
		account.Credentials["model_mapping"] = map[string]any{model: model}
		attachTestProtocolCapability(account, protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions)
		group := grp(1, PlatformAnthropic, 1, false)
		group.Hydrated = true
		resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{group}})
		gateway := &GatewayService{}
		wireCandidateTestResolver(resolver, gateway, []Account{*account})
		body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
		ctx := resolver.WithRequest(context.Background(), ShapeAnthropicMessages, "/v1/messages", model, body)
		_, err := gateway.evaluateGroupCandidates(ctx, nil, group, model, ShapeAnthropicMessages)
		require.ErrorIs(t, err, ErrUniversalUnsupportedModel)
		candidates := gateway.gatewayCandidates(ctx, []Account{*account}, PlatformAnthropic, false, model, nil)
		require.Empty(t, candidates)
		selected, err := resolver.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, model, "")
		require.Nil(t, selected)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrUniversalCapacityUnavailable)
	})

	for _, tc := range []struct {
		name         string
		schedulable  bool
		wantSelected bool
	}{
		{"mapped_claude", true, true},
		{"manual_off", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const model = "claude-sonnet-4-6"
			account := tokenseaAnthropicRelayAccount()
			account.ID, account.GroupIDs = 93, []int64{1}
			account.Status, account.Schedulable = StatusActive, tc.schedulable
			account.Credentials["api_key"] = "test-only"
			attachTestProtocolCapability(account, protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions)
			group := grp(1, PlatformAnthropic, 1, false)
			group.Hydrated = true
			resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{group}})
			gateway := &GatewayService{}
			wireCandidateTestResolver(resolver, gateway, []Account{*account})
			body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
			ctx := resolver.WithRequest(context.Background(), ShapeAnthropicMessages, "/v1/messages", model, body)
			state, err := gateway.evaluateGroupCandidates(ctx, nil, group, model, ShapeAnthropicMessages)
			require.NoError(t, err)
			candidates := gateway.gatewayCandidates(ctx, []Account{*account}, PlatformAnthropic, false, model, nil)
			selected, resolveErr := resolver.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, model, "")
			if !tc.schedulable {
				require.False(t, state.Supported)
				require.False(t, state.Available)
				require.Empty(t, candidates)
				require.Nil(t, selected)
				require.ErrorIs(t, resolveErr, ErrUniversalNoEntitledGroup)
				require.NotErrorIs(t, resolveErr, ErrUniversalCapacityUnavailable)
				return
			}
			require.True(t, state.Supported)
			require.Equal(t, tc.wantSelected, state.Available)
			require.NoError(t, resolveErr)
			require.Equal(t, group.ID, selected.ID)
			require.Len(t, candidates, 1)
			require.Equal(t, account.ID, candidates[0].ID)
		})
	}
}

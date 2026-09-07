//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestCandidateEligibilityTokenseaUsesPlanAcrossEntrances(t *testing.T) {
	const model = "gpt-5.6-luna"
	for _, tc := range []struct {
		name              string
		chat, schedulable bool
	}{
		{"legal_converter", true, true},
		{"disabled_capacity", true, false},
		{"messages_only_has_no_legal_route", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := tokenseaAnthropicRelayAccount()
			account.ID, account.GroupIDs = 93, []int64{1}
			account.Status, account.Schedulable = StatusActive, tc.schedulable
			account.Credentials["api_key"] = "test-only"
			protocols := []protocolrouter.Protocol{protocolrouter.ProtocolMessages}
			if tc.chat {
				protocols = append(protocols, protocolrouter.ProtocolChatCompletions)
			}
			attachTestProtocolCapability(account, protocols...)
			group := grp(1, PlatformAnthropic, 1, false)
			group.Hydrated = true
			resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{group}})
			gateway := &GatewayService{}
			wireCandidateTestResolver(resolver, gateway, []Account{*account})
			body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
			ctx := resolver.WithRequest(context.Background(), ShapeAnthropicMessages, "/v1/messages", model, body)
			state, err := gateway.evaluateGroupCandidates(ctx, nil, group, model, ShapeAnthropicMessages)
			require.NoError(t, err)
			require.Equal(t, tc.chat, state.Supported)
			require.Equal(t, tc.chat && tc.schedulable, state.Available)
			candidates := gateway.gatewayCandidates(ctx, []Account{*account}, PlatformAnthropic, false, model, nil)
			selected, err := resolver.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, model, "")
			switch {
			case !tc.chat:
				require.Empty(t, candidates)
				require.Nil(t, selected)
				require.ErrorIs(t, err, ErrUniversalNoEntitledGroup)
			case !tc.schedulable:
				require.Empty(t, candidates)
				require.Nil(t, selected)
				require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
			default:
				require.NoError(t, err)
				require.Equal(t, group.ID, selected.ID)
				require.Len(t, candidates, 1)
				require.Equal(t, account.ID, candidates[0].ID)
			}
		})
	}
}

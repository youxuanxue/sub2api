//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicpolicy"
	"github.com/stretchr/testify/require"
)

func TestCandidateThinkingToolsPreferExactAndFallbackOnCapacity(t *testing.T) {
	for _, unavailable := range []string{"", "full", "race", "excluded"} {
		t.Run(unavailable, func(t *testing.T) {
			groups := []Group{grp(10, PlatformOpenAI, 1, false)}
			accounts := []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 100, 10)}
			accounts[0].ProtocolEndpointCapability.ProbeEvidence.ModelCapabilities = map[string]map[protocolrouter.Protocol]anthropicpolicy.Capabilities{
				"gpt-5.4": {protocolrouter.ProtocolChatCompletions: {AlwaysThinking: true}},
			}
			r, _, key := globalCandidateFixture(groups, accounts)
			var acquired []int64
			cache := schedulerTestConcurrencyCache{acquiredIDs: &acquired, loadMap: map[int64]*AccountLoadInfo{}}
			if unavailable == "full" {
				cache.loadMap[2] = &AccountLoadInfo{AccountID: 2, CurrentConcurrency: 10}
			}
			if unavailable == "race" {
				cache.acquireResults = map[int64]bool{2: false}
			}
			r.candidateGateway.concurrencyService = NewConcurrencyService(cache)
			body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"tool_choice":"required"}`)
			ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, "", "")
			require.NoError(t, err)
			options := candidateSelectOptions{acquire: true}
			if unavailable == "excluded" {
				options.excluded = map[int64]struct{}{2: {}}
			}
			selection, err := state.selectAccount(ctx, options)
			require.NoError(t, err)
			defer selection.ReleaseFunc()
			want := int64(2)
			if unavailable != "" {
				want = 1
			}
			require.Equal(t, want, selection.Account.ID)
			require.Equal(t, body, state.body)
		})
	}
}

func TestExecuteSelectedProtocolRejectsChangedThinkingCapability(t *testing.T) {
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gpt-5.4", false, []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"tool_choice":"required"}`))
	require.NoError(t, err)
	router := NewProtocolRouter()
	ctx := WithProtocolRouting(context.Background(), router, request)
	account := protocolRoutingOpenAIAccount(12, "chat_completions")
	plan, _, err := protocolPlanForAccount(ctx, account, "gpt-5.4")
	require.NoError(t, err)
	fresh := *account
	capability := *account.ProtocolEndpointCapability
	capability.ProbeEvidence.ModelCapabilities = map[string]map[protocolrouter.Protocol]anthropicpolicy.Capabilities{"gpt-5.4": {protocolrouter.ProtocolChatCompletions: {AlwaysThinking: true}}}
	fresh.ProtocolEndpointCapability = &capability
	_, err = ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account, func(context.Context, *Account, string) error { return nil }, func(context.Context, int64) (*Account, error) { return &fresh, nil }, protocolExecutorsForTest(plan, func(context.Context, *Account, protocolrouter.Plan, protocolrouter.CanonicalRequest) (any, error) {
		t.Fatal("changed capability must be rejected before transport")
		return nil, nil
	}))
	require.ErrorIs(t, err, protocolrouter.ErrStalePlan)
}

func TestCandidateThinkingToolsRespectConversionPermission(t *testing.T) {
	for _, universal := range []bool{false, true} {
		for _, allowConversion := range []bool{false, true} {
			group := grp(10, PlatformOpenAI, 1, false)
			group.AllowMessagesDispatch = allowConversion
			account := globalCandidateAccount(1, 1, 10)
			account.Credentials["model_mapping"] = map[string]any{"claude-fable-5": "claude-fable-5"}
			attachTestProtocolCapability(&account, protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses)
			r, _, key := globalCandidateFixture([]Group{group}, []Account{account})
			if !universal {
				key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &group, &group.ID
			}
			body := []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`)
			ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicMessages, "/v1/messages", "claude-fable-5", body, "", "")
			require.NoError(t, err, "native Messages remains available when conversion is forbidden")
			selection, err := state.selectAccount(ctx, candidateSelectOptions{})
			require.NoError(t, err)
			want := protocolrouter.ProtocolMessages
			if allowConversion {
				want = protocolrouter.ProtocolResponses
			}
			require.Equal(t, want, selection.ProtocolPlan.TargetProtocol())
			_, err = ExecuteSelectedProtocol(ctx, r.router, selection, selection.Account,
				func(context.Context, *Account, string) error { return nil },
				func(context.Context, int64) (*Account, error) { return selection.Account, nil },
				protocolExecutorsForTest(*selection.ProtocolPlan, func(_ context.Context, _ *Account, plan protocolrouter.Plan, _ protocolrouter.CanonicalRequest) (any, error) {
					require.Equal(t, want, plan.TargetProtocol())
					return nil, nil
				}))
			require.NoError(t, err, "send-time planning uses the same conversion permission")
		}
	}
}

func TestCandidateThinkingToolsPreferAuthorizedExactOrigin(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)}
	groups[1].AllowMessagesDispatch = true
	account := globalCandidateAccount(1, 1, 10, 20)
	account.Credentials["model_mapping"] = map[string]any{"claude-fable-5": "claude-fable-5"}
	attachTestProtocolCapability(&account, protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses)
	r, _, key := globalCandidateFixture(groups, []Account{account})
	body := []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`)
	_, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicMessages, "/v1/messages", "claude-fable-5", body, "", "")
	require.NoError(t, err)
	require.Equal(t, int64(20), state.current.group.ID)
	require.Equal(t, protocolrouter.ProtocolResponses, state.current.plan.TargetProtocol())
}

func TestProtocolPlanCacheSeparatesConversionPermission(t *testing.T) {
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, "claude-fable-5", false, []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"any"}}`))
	require.NoError(t, err)
	account := protocolRoutingOpenAIAccount(1, "messages", "responses")
	account.Credentials["model_mapping"] = map[string]any{"claude-fable-5": "claude-fable-5"}
	ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
	for _, nativeOnly := range []bool{false, true, false} {
		ctx = withProtocolNativeOnly(ctx, nativeOnly)
		plan, governed, err := protocolPlanForAccount(ctx, account, "claude-fable-5")
		require.NoError(t, err)
		require.True(t, governed)
		want := protocolrouter.ProtocolResponses
		if nativeOnly {
			want = protocolrouter.ProtocolMessages
		}
		require.Equal(t, want, plan.TargetProtocol())
		selection, err := attachProtocolPlan(ctx, &AccountSelectionResult{Account: account})
		require.NoError(t, err)
		require.Equal(t, want, selection.ProtocolPlan.TargetProtocol())
	}
}

func TestCandidateThinkingToolsSlotRecheckPreservesConversionPermission(t *testing.T) {
	for _, mode := range []string{"direct", "universal"} {
		for _, stage := range []string{"acquire", "wait"} {
			for _, permission := range []string{"native-only", "conversion-allowed"} {
				t.Run(mode+"/"+stage+"/"+permission, func(t *testing.T) {
					group := grp(10, PlatformOpenAI, 1, false)
					group.AllowMessagesDispatch = permission == "conversion-allowed"
					account := globalCandidateAccount(1, 1, group.ID)
					account.Credentials["model_mapping"] = map[string]any{"claude-fable-5": "claude-fable-5"}
					attachTestProtocolCapability(&account, protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses)
					r, _, key := globalCandidateFixture([]Group{group}, []Account{account})
					if mode == "direct" {
						key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &group, &group.ID
					}
					body := []byte(`{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`)
					ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicMessages, "/v1/messages", "claude-fable-5", body, "", "")
					require.NoError(t, err)
					want := protocolrouter.ProtocolMessages
					if group.AllowMessagesDispatch {
						want = protocolrouter.ProtocolResponses
					}
					require.Equal(t, want, state.current.plan.TargetProtocol())
					if stage == "wait" {
						r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
							loadMap: map[int64]*AccountLoadInfo{account.ID: {AccountID: account.ID, CurrentConcurrency: account.Concurrency}},
						})
					}
					selection, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
					if selection != nil && selection.ReleaseFunc != nil {
						defer selection.ReleaseFunc()
					}
					require.NoError(t, err, "slot acquisition must preserve the authorized Plan")
					if stage == "wait" {
						require.NotNil(t, selection.WaitPlan)
						require.False(t, selection.Acquired)
						require.NoError(t, RecheckCandidateAccountSlot(ctx, account.ID), "post-wait recheck must preserve the authorized Plan")
					} else {
						require.True(t, selection.Acquired)
						require.Nil(t, selection.WaitPlan)
					}
					require.Equal(t, want, selection.ProtocolPlan.TargetProtocol())
					require.Equal(t, want, state.current.plan.TargetProtocol())
					require.Equal(t, body, state.body, "rechecking must preserve retry input")
				})
			}
		}
	}
}

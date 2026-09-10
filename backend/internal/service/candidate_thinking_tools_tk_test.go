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

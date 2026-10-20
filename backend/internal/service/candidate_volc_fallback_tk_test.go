//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func volcFallbackAccount(id int64) Account {
	a := globalCandidateAccount(id, 1, 10)
	a.Platform, a.ChannelType = PlatformNewAPI, newapiconstant.ChannelTypeVolcEngine
	a.Credentials["base_url"] = newapiintegration.VolcEngineAgentPlanBaseURL
	a.Credentials["model_mapping"] = map[string]any{"kimi-k3": "kimi-k3"}
	attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions)
	return a
}

func TestCandidateVolcFallbackSelection(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, scenario := range []string{"warm-nine", "full", "slot-race", "excluded", "ineligible", "priority", "recheck", "all-full", "continuation"} {
			t.Run(fmt.Sprintf("direct=%t/%s", direct, scenario), func(t *testing.T) {
				group := grp(10, PlatformNewAPI, 1, false)
				r, repo, key := globalCandidateFixture([]Group{group}, []Account{volcFallbackAccount(1), volcFallbackAccount(2), volcFallbackAccount(3)})
				if direct {
					key.RoutingMode, key.GroupID, key.Group = "direct", &group.ID, &group
				}
				cache := &candidateIdentityTestCache{}
				r.candidateGateway.cache = cache
				loads := map[int64]*AccountLoadInfo{1: {CurrentConcurrency: 10}, 2: {CurrentConcurrency: 7}, 3: {CurrentConcurrency: 1}}
				var acquired, released []int64
				cc := schedulerTestConcurrencyCache{loadMap: loads, acquiredIDs: &acquired, releasedIDs: &released}
				excluded := map[int64]struct{}{}
				want := int64(3)
				switch scenario {
				case "warm-nine":
					loads[1].CurrentConcurrency, want = 9, 1
				case "slot-race":
					loads[1].CurrentConcurrency = 9
					cc.acquireResults = map[int64]bool{1: false}
				case "excluded":
					loads[1].CurrentConcurrency = 0
					excluded[1] = struct{}{}
				case "ineligible":
					repo.accounts[0].Schedulable = false
				case "priority":
					repo.accounts[2].Priority, want = 2, 2
				case "recheck":
					want = 2
					repo.fresh = func(a *Account) *Account {
						if a.ID == 3 {
							copy := *a
							copy.Schedulable = false
							return &copy
						}
						return a
					}
				case "all-full":
					loads[2].CurrentConcurrency, loads[3].CurrentConcurrency, want = 10, 10, 1
				case "continuation":
					want = 1
				}
				r.candidateGateway.concurrencyService = NewConcurrencyService(cc)
				ctx, state := preparePromptCacheCandidate(t, r, key, "kimi-k3", "stable prefix", "question", "")
				if scenario == "continuation" {
					state.continuationAccountID = 1
				}
				for range 16 {
					acquired, released = nil, nil
					require.NoError(t, cache.SetSessionAccountID(ctx, 0, state.session, 1, 0))
					if state.promptCacheSession != "" {
						require.NoError(t, cache.SetSessionAccountID(ctx, 0, state.promptCacheSession, 1, 0))
					}
					result, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true, excluded: excluded})
					require.NoError(t, err)
					require.Equal(t, want, result.Account.ID)
					waiting := scenario == "all-full" || scenario == "continuation"
					require.Equal(t, !waiting, result.Acquired)
					if waiting {
						require.Equal(t, int64(1), result.WaitPlan.AccountID)
						require.Empty(t, acquired)
					} else {
						require.Nil(t, result.WaitPlan)
						result.ReleaseFunc()
					}
					bound, err := cache.GetSessionAccountID(ctx, 0, state.session)
					require.NoError(t, err)
					require.Equal(t, want, bound)
					if scenario == "slot-race" {
						require.Equal(t, []int64{1, 3}, acquired)
					}
					if scenario == "recheck" {
						require.Equal(t, []int64{3, 2}, acquired)
						require.Equal(t, []int64{3, 2}, released)
					}
				}
			})
		}
	}
}

func TestCandidateVolcFallbackOrderingBoundaries(t *testing.T) {
	for _, scenario := range []string{"same-tier", "missing-load", "negative-load", "no-binding", "continuation", "websocket", "other-shape", "equal-load"} {
		t.Run(scenario, func(t *testing.T) {
			group := grp(10, PlatformNewAPI, 1, false)
			state := &CandidateRequest{key: universalKey(7), shape: ShapeOpenAIChat, volcPlanAffinity: true}
			pool := make([]*candidateExecutionPath, 5)
			for i := range pool {
				a := volcFallbackAccount(int64(i + 1))
				pool[i] = &candidateExecutionPath{account: &a, group: &group}
			}
			pool[0].sticky = true
			// A different supplier stays in its existing position; it must not
			// make a pairwise load comparator non-transitive.
			pool[2].account.Credentials["base_url"] = "https://ark.cn-beijing.volces.com/api/v3"
			pool[4].account.Priority = 2
			loads := map[int64]*AccountLoadInfo{1: {CurrentConcurrency: 9}, 2: {CurrentConcurrency: 7}, 3: {CurrentConcurrency: 0}, 4: {CurrentConcurrency: 1}, 5: {CurrentConcurrency: 0}}
			want := []int64{1, 2, 3, 4, 5}
			switch scenario {
			case "same-tier":
				want = []int64{1, 4, 3, 2, 5}
			case "missing-load":
				delete(loads, 2)
			case "negative-load":
				loads[2].CurrentConcurrency = -1
			case "no-binding":
				state.volcPlanAffinity = false
			case "continuation":
				state.continuationAccountID = 1
			case "websocket":
				state.websocket = true
			case "other-shape":
				state.shape = ShapeAnthropicMessages
			case "equal-load":
				loads[2].CurrentConcurrency = 1
			}
			state.orderVolcPlanFallback(pool, loads, nil, nil)
			got := make([]int64, len(pool))
			for i, path := range pool {
				got[i] = path.account.ID
			}
			require.Equal(t, want, got)
		})
	}
}

func TestCandidateVolcFallbackLoadFailureKeepsSticky(t *testing.T) {
	group := grp(10, PlatformNewAPI, 1, false)
	r, _, key := globalCandidateFixture([]Group{group}, []Account{volcFallbackAccount(1), volcFallbackAccount(2)})
	cache := &candidateIdentityTestCache{}
	r.candidateGateway.cache = cache
	r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadBatchErr: errors.New("redis unavailable")})
	ctx, state := preparePromptCacheCandidate(t, r, key, "kimi-k3", "stable prefix", "question", "client-session")
	require.NoError(t, cache.SetSessionAccountID(context.Background(), 0, state.session, 1, 0))
	require.Equal(t, int64(1), acquirePromptCacheCandidate(t, ctx, state, nil))
}

func TestCandidateVolcFallbackBindingLifecycle(t *testing.T) {
	group := grp(10, PlatformNewAPI, 1, false)
	r, _, key := globalCandidateFixture([]Group{group}, []Account{volcFallbackAccount(1), volcFallbackAccount(2), volcFallbackAccount(3)})
	cache := &candidateIdentityTestCache{}
	r.candidateGateway.cache = cache
	loads := map[int64]*AccountLoadInfo{1: {CurrentConcurrency: 10}, 2: {CurrentConcurrency: 7}, 3: {CurrentConcurrency: 1}}
	cc := schedulerTestConcurrencyCache{loadMap: loads, acquireResults: map[int64]bool{2: false, 3: false}}
	r.candidateGateway.concurrencyService = NewConcurrencyService(cc)
	ctx, state := preparePromptCacheCandidate(t, r, key, "kimi-k3", "stable prefix", "question", "client-session")
	require.NoError(t, cache.SetSessionAccountID(ctx, 0, state.session, 1, 0))
	// Planning an alternative must not publish a binding before acquisition.
	planned, err := state.selectAccount(ctx, candidateSelectOptions{})
	require.NoError(t, err)
	require.Equal(t, int64(3), planned.Account.ID)
	bound, err := cache.GetSessionAccountID(ctx, 0, state.session)
	require.NoError(t, err)
	require.Equal(t, int64(1), bound)
	// Losing every alternative slot must still leave the original binding.
	waiting, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
	require.NoError(t, err)
	require.False(t, waiting.Acquired)
	bound, err = cache.GetSessionAccountID(ctx, 0, state.session)
	require.NoError(t, err)
	require.Equal(t, int64(1), bound)
	// A successful fallback becomes the affinity even after the old account
	// drains; an empty old account must not displace a ready warm binding.
	cc.acquireResults[3] = true
	require.Equal(t, int64(3), acquirePromptCacheCandidate(t, ctx, state, nil))
	loads[1].CurrentConcurrency, loads[3].CurrentConcurrency = 0, 9
	require.Equal(t, int64(3), acquirePromptCacheCandidate(t, ctx, state, nil))
}

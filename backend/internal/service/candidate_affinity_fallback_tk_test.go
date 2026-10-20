//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

func TestCandidateAffinityFallbackSelection(t *testing.T) {
	for _, supply := range []string{"volc", "openai", "mixed"} {
		for _, direct := range []bool{false, true} {
			for _, scenario := range []string{"warm-nine", "full", "slot-race", "excluded", "ineligible", "deleted", "priority", "unequal-capacity", "sticky-recheck", "recheck", "all-full", "continuation"} {
				t.Run(fmt.Sprintf("%s/direct=%t/%s", supply, direct, scenario), func(t *testing.T) {
					group := grp(10, PlatformNewAPI, 1, false)
					accounts := []Account{volcFallbackAccount(1), volcFallbackAccount(2), volcFallbackAccount(3)}
					for i := range accounts {
						if supply == "openai" || (supply == "mixed" && i == 2) {
							accounts[i] = globalCandidateAccount(int64(i+1), 1, 10)
						}
					}
					r, repo, key := globalCandidateFixture([]Group{group}, accounts)
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
					case "deleted":
						repo.accounts = repo.accounts[1:]
					case "priority":
						repo.accounts[2].Priority, want = 2, 2
					case "unequal-capacity":
						repo.accounts[1].Concurrency = 100
						loads[2].CurrentConcurrency = 7
						loads[3].CurrentConcurrency = 2
						want = 2
					case "sticky-recheck":
						loads[1].CurrentConcurrency = 9
						repo.fresh = func(a *Account) *Account {
							if a.ID == 1 {
								copy := *a
								copy.Schedulable = false
								return &copy
							}
							return a
						}
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
						if scenario == "slot-race" || scenario == "sticky-recheck" {
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
}

func TestCandidateAffinityFallbackOrderingBoundaries(t *testing.T) {
	for _, scenario := range []string{"same-tier", "missing-load", "negative-load", "zero-capacity", "negative-capacity", "no-binding", "continuation", "websocket", "messages", "gemini", "responses", "responses-compact", "unequal-capacity", "equal-ratio", "full-peer", "effective-priority"} {
		t.Run(scenario, func(t *testing.T) {
			group := grp(10, PlatformNewAPI, 1, false)
			state := &CandidateRequest{key: universalKey(7), shape: ShapeOpenAIChat, hasSoftAffinity: true}
			pool := make([]*candidateExecutionPath, 5)
			for i := range pool {
				a := globalCandidateAccount(int64(i+1), 1, 10)
				pool[i] = &candidateExecutionPath{account: &a, group: &group}
			}
			pool[0].sticky = true
			// Different suppliers participate in the same policy after eligibility.
			pool[1].account.Platform = PlatformAnthropic
			pool[2].account.Platform = PlatformGemini
			pool[3].account.Platform = PlatformNewAPI
			pool[4].account.Priority = 2
			loads := map[int64]*AccountLoadInfo{1: {CurrentConcurrency: 9}, 2: {CurrentConcurrency: 7}, 3: {CurrentConcurrency: 0}, 4: {CurrentConcurrency: 1}, 5: {CurrentConcurrency: 0}}
			want := []int64{1, 3, 4, 2, 5}
			var counts map[int64]int64
			switch scenario {
			case "missing-load":
				delete(loads, 2)
				want = []int64{1, 2, 3, 4, 5}
			case "negative-load":
				loads[2].CurrentConcurrency = -1
				want = []int64{1, 2, 3, 4, 5}
			case "zero-capacity", "negative-capacity":
				pool[1].account.Concurrency = 0
				if scenario == "negative-capacity" {
					pool[1].account.Concurrency = -1
				}
				want = []int64{1, 2, 3, 4, 5}
			case "no-binding":
				state.hasSoftAffinity = false
				want = []int64{1, 2, 3, 4, 5}
			case "continuation":
				state.continuationAccountID = 1
				want = []int64{1, 2, 3, 4, 5}
			case "websocket":
				state.websocket = true
			case "messages":
				state.shape = ShapeAnthropicMessages
			case "gemini":
				state.shape = ShapeGemini
			case "responses":
				state.path = "/v1/responses"
			case "responses-compact":
				state.path = "/v1/responses/compact"
			case "unequal-capacity":
				pool[1].account.Concurrency = 100
				loads[2].CurrentConcurrency = 10 // 10/100 beats 2/10 despite more in-flight requests.
				loads[4].CurrentConcurrency = 2
				want = []int64{1, 3, 2, 4, 5}
			case "equal-ratio":
				pool[1].account.Concurrency = 100
				loads[2].CurrentConcurrency = 10
				loads[3].CurrentConcurrency = 1
				want = []int64{1, 2, 3, 4, 5}
			case "full-peer":
				loads[3].CurrentConcurrency = 10
				want = []int64{1, 4, 3, 2, 5}
			case "effective-priority":
				counts = map[int64]int64{4: saturationThreshold}
				// Obtain the existing order before applying load, including any penalty.
				sort.SliceStable(pool, func(i, j int) bool { return candidatePathLess(state.key, state.shape, counts, nil, pool[i], pool[j]) })
				want = []int64{1, 3, 2, 5, 4}
			}
			state.orderAffinityFallback(pool, loads, counts, nil)
			got := make([]int64, len(pool))
			for i, path := range pool {
				got[i] = path.account.ID
			}
			require.Equal(t, want, got)
		})
	}
}

func TestCandidateAffinityFallbackLoadFailureKeepsSticky(t *testing.T) {
	group := grp(10, PlatformNewAPI, 1, false)
	r, _, key := globalCandidateFixture([]Group{group}, []Account{volcFallbackAccount(1), volcFallbackAccount(2)})
	cache := &candidateIdentityTestCache{}
	r.candidateGateway.cache = cache
	r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadBatchErr: errors.New("redis unavailable")})
	ctx, state := preparePromptCacheCandidate(t, r, key, "kimi-k3", "stable prefix", "question", "client-session")
	require.NoError(t, cache.SetSessionAccountID(context.Background(), 0, state.session, 1, 0))
	require.Equal(t, int64(1), acquirePromptCacheCandidate(t, ctx, state, nil))
}

func TestCandidateAffinityFallbackBindingLifecycle(t *testing.T) {
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

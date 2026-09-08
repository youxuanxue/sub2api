//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

type globalCandidateRepo struct {
	stubOpenAIAccountRepo
	listed []int64
	fresh  func(*Account) *Account
}

func (r *globalCandidateRepo) ListCandidateAccounts(_ context.Context, groups []int64) ([]Account, error) {
	r.listed = append([]int64(nil), groups...)
	return r.accounts, nil
}

func (r *globalCandidateRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	a, err := r.stubOpenAIAccountRepo.GetByID(ctx, id)
	if a != nil && r.fresh != nil {
		a = r.fresh(a)
	}
	return a, err
}

func globalCandidateFixture(groups []Group, accounts []Account) (*UniversalRoutingResolver, *globalCandidateRepo, *APIKey) {
	repo := &globalCandidateRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: accounts}}
	gw := &GatewayService{accountRepo: repo}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: groups})
	r.router = NewProtocolRouter()
	r.candidateGateway = gw
	r.candidateOpenAI = &OpenAIGatewayService{accountRepo: repo}
	key := universalKey(7)
	key.User = &User{ID: 7, Balance: 100}
	return r, repo, key
}

func prepareGlobalCandidate(t *testing.T, r *UniversalRoutingResolver, key *APIKey) (context.Context, *CandidateRequest) {
	t.Helper()
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`)
	ctx, request, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, "", "")
	require.NoError(t, err)
	return ctx, request
}

func globalCandidateAccount(id int64, priority int, groups ...int64) Account {
	a := *protocolRoutingOpenAIAccount(id, "chat_completions")
	a.Priority, a.Concurrency, a.GroupIDs = priority, 10, groups
	return a
}

func TestGlobalCandidateAccountPriorityIgnoresGroupTopology(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		groups := []Group{grp(10, PlatformAnthropic, -100, false), grp(20, PlatformGemini, 100, false)}
		accounts := []Account{globalCandidateAccount(1, 10, 10), globalCandidateAccount(2, 1, 20)}
		if duplicate {
			groups = append(groups, grp(30, PlatformGrok, -999, false))
			accounts[0].GroupIDs = append(accounts[0].GroupIDs, 30)
			groups[0], groups[1] = groups[1], groups[0]
		}
		r, _, key := globalCandidateFixture(groups, accounts)
		ctx, state := prepareGlobalCandidate(t, r, key)
		require.Equal(t, int64(2), state.current.account.ID)
		selection, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", nil, "", key.UserID)
		require.NoError(t, err)
		require.Equal(t, int64(2), selection.Account.ID)
		require.NotNil(t, selection.ProtocolPlan)
		require.Equal(t, int64(20), *key.GroupID)
		selection.ReleaseFunc()
	}
}

func TestGlobalCandidateDeduplicatesMembershipBeforeSelection(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)}
	groups[0].RateMultiplier, groups[1].RateMultiplier = 2, 0.5
	r, _, key := globalCandidateFixture(groups, []Account{globalCandidateAccount(1, 1, 10, 20)})
	ctx, state := prepareGlobalCandidate(t, r, key)
	paths, _, err := state.candidates(ctx, candidateSelectOptions{})
	require.NoError(t, err)
	require.Len(t, paths, 1)
	require.Equal(t, int64(20), paths[0].group.ID)
}

func TestGlobalCandidateReadyPeerAndSlotRace(t *testing.T) {
	for _, race := range []bool{false, true} {
		groups := []Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)}
		r, _, key := globalCandidateFixture(groups, []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 10, 20)})
		var acquired []int64
		cache := schedulerTestConcurrencyCache{acquiredIDs: &acquired, loadMap: map[int64]*AccountLoadInfo{}}
		if race {
			cache.acquireResults = map[int64]bool{1: false}
		} else {
			cache.loadMap[1] = &AccountLoadInfo{AccountID: 1, CurrentConcurrency: 10}
		}
		r.candidateGateway.concurrencyService = NewConcurrencyService(cache)
		ctx, _ := prepareGlobalCandidate(t, r, key)
		result, _, err := r.candidateOpenAI.SelectAccountWithScheduler(ctx, key.GroupID, "", "", "gpt-5.4", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.Equal(t, int64(2), result.Account.ID)
		require.True(t, result.Acquired)
		require.Nil(t, result.WaitPlan)
		result.ReleaseFunc()
		if race {
			require.Equal(t, []int64{1, 2}, acquired)
		} else {
			require.Equal(t, []int64{2}, acquired)
		}
	}
}

func TestGlobalCandidateFreshRevocationReleasesSlot(t *testing.T) {
	r, repo, key := globalCandidateFixture([]Group{grp(10, PlatformOpenAI, 1, false)}, []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 10)})
	var released []int64
	r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{releasedIDs: &released})
	ctx, _ := prepareGlobalCandidate(t, r, key)
	repo.fresh = func(a *Account) *Account {
		if a.ID == 1 {
			copy := *a
			copy.GroupIDs = nil
			copy.AccountGroups = nil
			return &copy
		}
		return a
	}
	result, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", nil, "", key.UserID)
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Account.ID)
	require.Equal(t, []int64{1}, released)
	result.ReleaseFunc()
}

func TestGlobalCandidateRebindingFailureNeverReturnsAcquiredAccount(t *testing.T) {
	r, _, key := globalCandidateFixture([]Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)}, []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 20)})
	var released []int64
	r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{releasedIDs: &released})
	ctx, _ := prepareGlobalCandidate(t, r, key)
	denied := errors.New("reservation denied")
	SetCandidateBillingHook(ctx, func() error { return denied })
	result, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", map[int64]struct{}{1: {}}, "", key.UserID)
	require.ErrorIs(t, err, denied)
	require.Nil(t, result)
	require.Equal(t, []int64{2}, released)
}

func TestGlobalCandidateWindowRecoveryRunsAfterUnion(t *testing.T) {
	reserve := &candidateExecutionPath{account: &Account{ID: 1}, reserve: true}
	ready := &candidateExecutionPath{account: &Account{ID: 2}}
	require.Equal(t, []*candidateExecutionPath{ready}, recoverCandidateWindowPool([]*candidateExecutionPath{reserve, ready}))
	ready.reserve = true
	require.Len(t, recoverCandidateWindowPool([]*candidateExecutionPath{reserve, ready}), 2)
}

func TestGlobalCandidateNativeMessagesDoesNotNeedConversionPermission(t *testing.T) {
	account := globalCandidateAccount(115, 1, 1)
	account.Platform, account.ChannelType = PlatformNewAPI, 14
	account.Credentials["model_mapping"] = map[string]any{"claude-fable-5": "claude-fable-5"}
	attachTestProtocolCapability(&account, protocolrouter.ProtocolMessages)
	r, _, key := globalCandidateFixture([]Group{grp(1, PlatformAnthropic, 1, false)}, []Account{account})
	body := []byte(`{"model":"claude-fable-5","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	_, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicMessages, "/v1/messages", "claude-fable-5", body, "", "")
	require.NoError(t, err)
	require.Equal(t, protocolrouter.ProtocolMessages, state.current.plan.TargetProtocol())
	account.Credentials["model_mapping"] = map[string]any{"claude-fable-5": "gpt-5.4"}
	attachTestProtocolCapability(&account, protocolrouter.ProtocolChatCompletions)
	r, _, key = globalCandidateFixture([]Group{grp(1, PlatformAnthropic, 1, false)}, []Account{account})
	_, _, err = r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicMessages, "/v1/messages", "claude-fable-5", body, "", "")
	require.Error(t, err)
}

func TestGlobalCandidateWaitRechecksAuthorizationAndPlan(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		r, repo, key := globalCandidateFixture([]Group{grp(10, PlatformOpenAI, 1, false)}, []Account{globalCandidateAccount(1, 1, 10)})
		r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
			loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 10}},
		})
		ctx, state := prepareGlobalCandidate(t, r, key)
		result, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
		require.NoError(t, err)
		require.NotNil(t, result.WaitPlan)
		repo.fresh = func(a *Account) *Account {
			copy := *a
			if revoke {
				copy.GroupIDs = nil
			} else {
				copy.Credentials = map[string]any{}
				for k, v := range a.Credentials {
					copy.Credentials[k] = v
				}
				copy.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.4-mini"}
			}
			return &copy
		}
		err = RecheckCandidateAccountSlot(ctx, 1)
		if revoke {
			require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
		} else {
			require.ErrorIs(t, err, protocolrouter.ErrStalePlan)
			require.Equal(t, "gpt-5.4", result.ProtocolPlan.ResolvedModel())
		}
	}
}

func TestGlobalCandidateDirectCompositeAliasPrecedesPlan(t *testing.T) {
	group := grp(10, PlatformComposite, 1, false)
	account := globalCandidateAccount(1, 1, 10)
	account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.4"}
	r, _, key := globalCandidateFixture([]Group{group}, []Account{account})
	r.candidateGateway.compositeResolver = NewCompositeRouteResolver(compositeRouteRepoStub{routes: []CompositeModelRoute{{
		GroupID: 10, Enabled: true, MatchType: CompositeRouteMatchExact, PublicModel: "legacy-client-model", UpstreamModel: "gpt-5.4",
		Endpoint: CompositeRouteEndpointChatCompletions, TargetPlatform: PlatformAnthropic,
	}}})
	key.RoutingMode, key.GroupID, key.Group = "direct", &group.ID, &group
	body := []byte(`{"model":"legacy-client-model","messages":[{"role":"user","content":"hello"}]}`)
	_, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "legacy-client-model", body, "", "")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", state.current.plan.ResolvedModel())
	require.Equal(t, PlatformOpenAI, state.current.account.Platform)
	key.RoutingMode = "universal"
	_, _, err = r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "legacy-client-model", body, "", "")
	require.ErrorIs(t, err, ErrUniversalUnsupportedModel)
}

func TestGlobalCandidateNormalizedOccupancyAndTopologyTies(t *testing.T) {
	for _, split := range []bool{false, true} {
		groups := []Group{grp(10, PlatformAnthropic, -100, false)}
		accounts := []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 1, 10)}
		accounts[0].Concurrency, accounts[1].Concurrency = 100, 10
		if split {
			groups = []Group{grp(900, PlatformGrok, -999, false), grp(800, PlatformGemini, 999, false)}
			groups[0].Name, groups[1].Name = "renamed", "split"
			accounts[0].GroupIDs, accounts[1].GroupIDs = []int64{900, 800, 900}, []int64{800}
		}
		r, _, key := globalCandidateFixture(groups, accounts)
		loads := map[int64]*AccountLoadInfo{1: {CurrentConcurrency: 10}, 2: {CurrentConcurrency: 2}}
		r.candidateGateway.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: loads})
		ctx, state := prepareGlobalCandidate(t, r, key)
		result, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
		require.NoError(t, err)
		require.Equal(t, int64(1), result.Account.ID, "10/100 is less occupied than 2/10")
		result.ReleaseFunc()
		loads[2].CurrentConcurrency = 1
		wins := map[int64]int{}
		for range 128 {
			result, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
			require.NoError(t, err)
			wins[result.Account.ID]++
			result.ReleaseFunc()
		}
		// Wide bounds catch persistent group/ID preference without depending on
		// a particular random sequence (tail probability below 10^-14).
		require.Greater(t, wins[1], 20)
		require.Greater(t, wins[2], 20)
		paths, _, err := state.candidates(ctx, candidateSelectOptions{})
		require.NoError(t, err)
		require.Len(t, paths, 2, "duplicated grants cannot create extra scheduling votes")
	}
}

func TestGlobalCandidateModelGrantsCannotBeBorrowed(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformGemini, 2, false)}
	accounts := []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 20, 20)}
	accounts[0].Credentials["model_mapping"] = map[string]any{"gpt-4.1": "gpt-4.1"}
	accounts[1].Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.4"}
	r, _, key := globalCandidateFixture(groups, accounts)
	ctx, _ := prepareGlobalCandidate(t, r, key)
	selected, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", nil, "", key.UserID)
	require.NoError(t, err)
	require.Equal(t, int64(2), selected.Account.ID)
	require.Equal(t, int64(20), *key.GroupID)
	selected.ReleaseFunc()
	selected, err = r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", map[int64]struct{}{2: {}}, "", key.UserID)
	require.ErrorIs(t, err, ErrUniversalUnsupportedModel)
	require.Nil(t, selected)
}

func TestGlobalCandidateWindowRecoveryAndStickyAdmission(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, -100, false), grp(20, PlatformAnthropic, 100, false), grp(30, PlatformGemini, -999, false)}
	accounts := []Account{globalCandidateAccount(1, 1, 10, 30), globalCandidateAccount(2, 20, 20)}
	accounts[0].Extra = map[string]any{"codex_5h_used_percent": 99.0, "codex_usage_updated_at": time.Now().Format(time.RFC3339)}
	r, repo, key := globalCandidateFixture(groups, accounts)
	r.candidateGateway.cache = &stubGatewayCache{sessionBindings: map[string]int64{"existing": 1}}
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`)
	for _, session := range []string{"new", "existing"} {
		ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, session, "")
		require.NoError(t, err)
		want := int64(2)
		if session == "existing" {
			want = 1
		}
		require.Equal(t, want, state.current.account.ID, "pre-billing admission must recognize a validated existing session")
		selected, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, session, "gpt-5.4", nil, "", key.UserID)
		require.NoError(t, err)
		require.Equal(t, want, selected.Account.ID)
		selected.ReleaseFunc()
	}
	repo.accounts[1].Schedulable = false
	ctx, state := prepareGlobalCandidate(t, r, key)
	selected, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
	require.NoError(t, err)
	require.Equal(t, int64(1), selected.Account.ID, "only a globally empty normal pool can recover the reserve")
	selected.ReleaseFunc()
	repo.accounts[0].Schedulable = false
	_, _, err = r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, "", "")
	require.ErrorIs(t, err, ErrUniversalCapacityUnavailable, "recovery cannot bypass hard gates")
}

func TestGlobalCandidateNativeConverterParityAndGoogleFailover(t *testing.T) {
	_, _, googleAccounts, request := candidateGoogleFixture(t)
	groups := []Group{grp(16, PlatformAnthropic, 1, false), grp(21, PlatformOpenAI, 2, false)}
	r, _, key := globalCandidateFixture(groups, googleAccounts)
	ctx, _, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", request.RequestedModel(), request.Body(), "", "")
	require.NoError(t, err)
	for _, excluded := range []int64{62, 74} {
		result, err := r.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", request.RequestedModel(), map[int64]struct{}{excluded: {}}, "", key.UserID)
		require.NoError(t, err)
		require.NotEqual(t, excluded, result.Account.ID)
		require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, result.ProtocolPlan.TargetProtocol())
		result.ReleaseFunc()
	}
	accounts := []Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 1, 20)}
	for i := range accounts {
		accounts[i].Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}
	}
	attachTestProtocolCapability(&accounts[1], protocolrouter.ProtocolMessages)
	r, _, key = globalCandidateFixture([]Group{grp(10, PlatformAnthropic, 1, false), grp(20, PlatformGemini, 2, false)}, accounts)
	ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "claude-sonnet-4-6", []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`), "", "")
	require.NoError(t, err)
	paths, _, err := state.candidates(ctx, candidateSelectOptions{})
	require.NoError(t, err)
	require.Len(t, paths, 2)
	wins := map[protocolrouter.Protocol]int{}
	for range 128 {
		result, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
		require.NoError(t, err)
		wins[result.ProtocolPlan.TargetProtocol()]++
		result.ReleaseFunc()
	}
	require.Greater(t, wins[protocolrouter.ProtocolChatCompletions], 20)
	require.Greater(t, wins[protocolrouter.ProtocolMessages], 20)
}

func TestCandidateResponsesImagePermissionPrecedesBillingOrigin(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		groups := []Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)}
		groups[0].AllowImageGeneration = false
		groups[0].RateMultiplier, groups[1].RateMultiplier = .1, 1
		account := globalCandidateAccount(1, 1, 10, 20)
		attachTestProtocolCapability(&account, protocolrouter.ProtocolResponses)
		r, _, key := globalCandidateFixture(groups, []Account{account})
		body := []byte(`{"model":"gpt-5.4","input":"hello","tools":[{"type":"namespace","name":"image_gen","tools":[]}]}`)
		if explicit {
			body = []byte(`{"model":"gpt-5.4","input":"draw a tree","tools":[{"type":"image_generation"}]}`)
		}
		ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/responses", "gpt-5.4", body, "", "")
		require.NoError(t, err)
		want := int64(10)
		if explicit {
			want = 20
		}
		require.Equal(t, want, *key.GroupID)
		selected, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
		require.NoError(t, err)
		require.Equal(t, want, *key.GroupID)
		selected.ReleaseFunc()
	}
}

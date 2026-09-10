//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type candidateDiscoveryParentRepo struct {
	*globalCandidateRepo
	parent      *Account
	parentError error
	parentReads int
}

func (r *candidateDiscoveryParentRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.parentReads++
	if r.parentError != nil {
		return nil, r.parentError
	}
	if r.parent != nil && r.parent.ID == id {
		return r.parent, nil
	}
	return nil, errors.New("unexpected account read")
}

func TestCandidateDiscoveryReadsSharedShadowParentOnceAndRefreshesNextCall(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 0, false), grp(20, PlatformOpenAI, 0, false)}
	parent := &Account{ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	accounts := []Account{globalCandidateAccount(1, 1, 10, 20), globalCandidateAccount(2, 1, 10, 20)}
	for i := range accounts {
		accounts[i].ParentAccountID = &parent.ID
		accounts[i].Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.4", "gpt-5.4-mini": "gpt-5.4-mini"}
	}
	svc, key := candidateDiscoveryFixture(groups, accounts)
	repo := &candidateDiscoveryParentRepo{globalCandidateRepo: svc.resolver.candidateGateway.accountRepo.(*globalCandidateRepo), parent: parent}
	svc.resolver.candidateGateway.accountRepo = repo
	models, _, err := svc.DiscoverCandidates(context.Background(), key, UniversalProtocolAll)
	require.NoError(t, err)
	require.Len(t, models, 2)
	require.Equal(t, 1, repo.parentReads, "all models, shapes and sibling shadows share one parent read")

	repo.parentError = errors.New("parent unavailable on next request")
	_, _, err = svc.DiscoverCandidates(context.Background(), key, UniversalProtocolAll)
	require.ErrorIs(t, err, repo.parentError)
	require.Equal(t, 2, repo.parentReads, "a fresh discovery must read the parent again")
}

func TestCandidateDiscoveryPreparedPathSeparatesGroupAndRequestPolicy(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 0, false), grp(20, PlatformOpenAI, 0, false)}
	groups[0].AllowMessagesDispatch = true
	groups[1].AllowMessagesDispatch = true
	groups[0].MessagesDispatchModelConfig.ExactModelMappings = map[string]string{"alias": "gpt-5.4"}
	groups[1].MessagesDispatchModelConfig.ExactModelMappings = map[string]string{"alias": "gpt-5.4-mini"}
	r, _, key := globalCandidateFixture(groups, nil)
	key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &groups[0], &groups[0].ID
	request := &CandidateRequest{resolver: r, key: key, shape: ShapeOpenAIChat, path: "/v1/chat/completions", model: "alias", body: []byte(`{"model":"alias","messages":[]}`)}
	prepare := candidateDiscoveryPathPreparer(request)
	ctxA, modelA, _, err := prepare(context.Background(), &groups[0])
	require.NoError(t, err)
	ctxAgain, _, _, err := prepare(context.Background(), &groups[0])
	require.NoError(t, err)
	require.Same(t, ctxA, ctxAgain)
	_, modelB, _, err := prepare(context.Background(), &groups[1])
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", modelA)
	require.Equal(t, "gpt-5.4-mini", modelB)

	groups[0].MessagesDispatchModelConfig.ExactModelMappings["alias"] = "gpt-5.4-mini"
	fresh := candidateDiscoveryPathPreparer(request)
	_, freshModel, _, err := fresh(context.Background(), &groups[0])
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4-mini", freshModel)
}

func TestCandidateDiscoveryPlanCacheAvoidsRepeatedSnapshotsAndRefreshesNextRequest(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 0, false)}
	account := globalCandidateAccount(1, 1, 10)
	r, _, key := globalCandidateFixture(groups, []Account{account})
	request := &CandidateRequest{resolver: r, key: key, shape: ShapeOpenAIChat, path: "/v1/chat/completions", model: "gpt-5.4", body: []byte(`{"model":"gpt-5.4","messages":[]}`)}
	discovery, model, _, err := candidateDiscoveryPathPreparer(request)(context.Background(), &groups[0])
	require.NoError(t, err)
	execution, _, _, err := request.pathContext(context.Background(), &groups[0])
	require.NoError(t, err)
	for _, ctx := range []context.Context{discovery, execution} {
		_, governed, planErr := protocolPlanForAccount(ctx, &account, model)
		require.True(t, governed)
		require.NoError(t, planErr)
	}
	allocations := func(ctx context.Context) float64 {
		return testing.AllocsPerRun(20, func() {
			_, _, planErr := protocolPlanForAccount(ctx, &account, model)
			if planErr != nil {
				t.Fatal(planErr)
			}
		})
	}
	require.Less(t, allocations(discovery), allocations(execution), "metadata cache hits must avoid rebuilding endpoint/account snapshots")
	account.ProtocolEndpointCapability = nil
	_, _, err = protocolPlanForAccount(execution, &account, model)
	require.Error(t, err, "execution must still validate current account evidence even with a cached Plan")
	fresh, _, _, err := candidateDiscoveryPathPreparer(request)(context.Background(), &groups[0])
	require.NoError(t, err)
	_, _, err = protocolPlanForAccount(fresh, &account, model)
	require.Error(t, err, "the next discovery request must not inherit the old account snapshot")
}

func BenchmarkCandidateDiscoverySnapshot(b *testing.B) {
	groups := make([]Group, 6)
	for i := range groups {
		groups[i] = grp(int64(i+1), PlatformOpenAI, 0, false)
		groups[i].AllowMessagesDispatch = true
	}
	accounts := make([]Account, 24)
	for i := range accounts {
		accounts[i] = globalCandidateAccount(int64(i+1), 1, 1, 2, 3, 4, 5, 6)
		mapping := make(map[string]any, 30)
		for j := 0; j < 30; j++ {
			model := fmt.Sprintf("gpt-5.4-model-%02d", j)
			mapping[model] = model
		}
		accounts[i].Credentials["model_mapping"] = mapping
	}
	svc, key := candidateDiscoveryFixture(groups, accounts)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		models, _, err := svc.DiscoverCandidates(context.Background(), key, UniversalProtocolAll)
		if err != nil || len(models) != 30 {
			b.Fatalf("discovery = %d models, %v", len(models), err)
		}
	}
}

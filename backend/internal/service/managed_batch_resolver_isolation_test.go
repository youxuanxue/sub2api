//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/batchadapter"
	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type managedItemKeySource struct {
	key   *APIKey
	owner *UniversalRoutingResolver
}

func (s managedItemKeySource) GetByID(context.Context, int64) (*APIKey, error) { return s.key, nil }
func (s managedItemKeySource) UniversalResolver() *UniversalRoutingResolver    { return s.owner }

type managedItemSubscriptionRepo struct {
	UserSubscriptionRepository
	sub *UserSubscription
}

func (s managedItemSubscriptionRepo) GetActiveByUserIDAndGroupID(_ context.Context, userID, groupID int64) (*UserSubscription, error) {
	if s.sub.UserID != userID || s.sub.GroupID != groupID {
		return nil, ErrSubscriptionNotFound
	}
	return s.sub, nil
}

func TestManagedBatchResolverItemIsolation(t *testing.T) {
	a, b := grp(10, PlatformOpenAI, 0, false), grp(20, PlatformOpenAI, 0, true)
	a.RPMLimit, b.RPMLimit = 10, 10
	accounts := []Account{globalCandidateAccount(3, 1, a.ID), globalCandidateAccount(5, 1, b.ID)}
	for i, m := range []string{"gpt-4.1", "gpt-5.4"} {
		accounts[i].Credentials["model_mapping"] = map[string]any{m: m}
	}
	owner, _, key := globalCandidateFixture([]Group{a, b}, accounts)
	key.Status, key.User.Status = StatusActive, StatusActive
	sub := &UserSubscription{ID: 83, UserID: key.UserID, GroupID: b.ID, Status: SubscriptionStatusActive, ExpiresAt: time.Now().Add(time.Hour)}
	key.User.RPMLimit = 10
	rpm := &candidateRPMCache{}
	cache := &candidateQuotaCache{subscription: &SubscriptionCacheData{Status: sub.Status, ExpiresAt: sub.ExpiresAt}}
	cache.balance = 100
	billing := NewBillingCacheService(cache, nil, nil, nil, rpm, nil, &config.Config{}, nil)
	t.Cleanup(billing.Stop)
	owner.candidateGateway.billingCacheService = billing
	owner.candidateSubscriptions = &SubscriptionService{userSubRepo: managedItemSubscriptionRepo{sub: sub}}
	resolver := &ManagedBatchExecutionResolver{APIKeys: managedItemKeySource{key, owner}, Gateway: owner.candidateOpenAI, Router: owner.router}
	req := batchadapter.ExecutionRequest{BatchID: "batch", UserID: key.UserID, APIKeyID: key.ID}
	batch := batchadapter.ExecutionBatch{ID: req.BatchID, UserID: req.UserID, APIKeyID: req.APIKeyID, Endpoint: batchcontract.EndpointResponses}
	resolve := func(id int64, model string) (batchadapter.ExecutionSelection, error) {
		item := batchadapter.ExecutionItem{ID: id, BatchID: batch.ID, CustomID: model}
		line := batchcontract.RequestLine{CustomID: item.CustomID, Method: "POST", URL: batch.Endpoint, Body: json.RawMessage(`{"model":"` + model + `","input":"hello"}`)}
		return resolver.Resolve(context.Background(), req, batch, item, line)
	}
	first, err := resolve(1, "gpt-5.4")
	require.NoError(t, err)
	defer first.Release()
	second, err := resolve(2, "gpt-4.1")
	require.NoError(t, err)
	defer second.Release()
	for _, tc := range []struct {
		selection      batchadapter.ExecutionSelection
		group, account int64
		subscription   *UserSubscription
	}{{first, b.ID, 5, sub}, {second, a.ID, 3, nil}} {
		execution, ok := ManagedBatchExecutionFromContext(tc.selection.Context)
		require.True(t, ok)
		require.Equal(t, tc.group, *execution.APIKey.GroupID)
		require.Equal(t, tc.account, execution.Selection.ProtocolPlan.AccountID())
		require.Equal(t, tc.account, tc.selection.AccountID)
		require.Equal(t, batchadapter.ProviderOpenAI, tc.selection.Provider)
		require.Equal(t, tc.subscription, execution.Subscription)
		require.Empty(t, QuotaPlatform(tc.selection.Context, execution.APIKey))
		require.NotSame(t, key, execution.APIKey)
	}
	require.Nil(t, key.GroupID)
	require.Nil(t, key.Group)
	key.RoutingMode = RoutingModeDirect
	key.GroupID, key.Group = &a.ID, &a
	_, err = resolve(3, "gpt-5.4")
	require.Error(t, err) // account 5 is outside direct key's group
	direct, err := resolve(4, "gpt-4.1")
	require.NoError(t, err)
	defer direct.Release()
	require.Equal(t, int64(3), direct.AccountID)
	require.Equal(t, PlatformOpenAI, QuotaPlatform(direct.Context, key))
	var released []int64
	queue := NewConcurrencyService(schedulerTestConcurrencyCache{waitCounts: map[int64]int{3: 1}, releasedIDs: &released})
	owner.candidateGateway.concurrencyService, resolver.Gateway.concurrencyService = queue, queue
	_, err = resolve(8, "gpt-4.1")
	require.ErrorIs(t, err, ErrManagedBatchCapacity)
	require.Equal(t, []int64{3}, released, "interactive waiters take precedence; batch cannot keep the slot")
	require.EqualValues(t, 3, rpm.userGroupCalls, "count each dispatched item once, not candidate projections or busy attempts")
	require.EqualValues(t, 3, rpm.userCalls)
	key.Group = &b
	_, err = resolve(5, "gpt-5.4")
	require.ErrorIs(t, err, ErrManagedBatchUnauthorized)
	key.Group = &a
	key.User.Status = StatusDisabled
	_, err = resolve(6, "gpt-4.1")
	require.ErrorIs(t, err, ErrManagedBatchUnauthorized)
	key.User.Status = StatusActive
	req.UserID++
	batch.UserID = req.UserID
	_, err = resolve(7, "gpt-4.1")
	require.ErrorIs(t, err, ErrManagedBatchUnauthorized)
}

//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type countedMenuCatalog struct {
	fakeCatalogProvider
	calls int
}

func (f *countedMenuCatalog) BuildPublicCatalog(ctx context.Context) *PublicCatalogResponse {
	f.calls++
	return f.fakeCatalogProvider.BuildPublicCatalog(ctx)
}

type countedMenuChannels struct {
	fakeChannelLister
	calls int
}

func (f *countedMenuChannels) ListAvailable(ctx context.Context) ([]AvailableChannel, error) {
	f.calls++
	return f.fakeChannelLister.ListAvailable(ctx)
}

type countedMenuAvailabilityRepo struct {
	*memoryAvailabilityRepo
	singles int
	batches int
	err     error
}

func (r *countedMenuAvailabilityRepo) Get(ctx context.Context, p, m string) (AvailabilityState, error) {
	r.singles++
	return r.memoryAvailabilityRepo.Get(ctx, p, m)
}

func (r *countedMenuAvailabilityRepo) GetBatch(ctx context.Context, p string, ids []string) (map[string]AvailabilityState, error) {
	r.batches++
	if r.err != nil {
		return nil, r.err
	}
	out := make(map[string]AvailabilityState, len(ids))
	for _, id := range ids {
		out[id], _ = r.memoryAvailabilityRepo.Get(ctx, p, id)
	}
	return out, nil
}

func TestMePricingMenuBatchesAvailabilityAndReusesInputs(t *testing.T) {
	ids := firstNPlatformServableIDsForSelfHealTest(t, PlatformOpenAI, 3)
	groups := []Group{mkGroupForMe(10, "one", PlatformOpenAI, 1), mkGroupForMe(20, "two", PlatformOpenAI, 1), mkGroupForMe(30, "three", PlatformOpenAI, 1)}
	channels := &countedMenuChannels{}
	catalog := &countedMenuCatalog{fakeCatalogProvider: fakeCatalogProvider{resp: &PublicCatalogResponse{}}}
	models := make([]SupportedModel, 0, len(ids))
	mapping := map[string]any{}
	for _, id := range ids {
		models = append(models, mkSupportedModel(id, PlatformOpenAI, mkPricing(1, 2, 0)))
		catalog.resp.Data = append(catalog.resp.Data, mkPublicCatalogModel(id, "openai", 1, 2, 0))
		mapping[id] = id
	}
	channels.channels = []AvailableChannel{mkChannelWithModel(1, "shared", []AvailableGroupRef{{ID: 10}, {ID: 20}, {ID: 30}}, models)}
	accounts := make([]Account, 8)
	for i := range accounts {
		accounts[i] = Account{ID: int64(i + 1), Platform: PlatformOpenAI, Type: "apikey", Credentials: map[string]any{"model_mapping": mapping}}
	}
	repo := &countedMenuAvailabilityRepo{memoryAvailabilityRepo: newMemoryRepo()}
	seedAvail(repo.memoryAvailabilityRepo, PlatformOpenAI, ids[0], AvailabilityStatusUnreachable, FailureKindModelNotFound)
	seedAvail(repo.memoryAvailabilityRepo, PlatformOpenAI, ids[1], AvailabilityStatusUnreachable, FailureKindUpstream5xx)
	svc := newServiceWithAccounts(&fakeKeyAccess{groups: groups}, nil, nil, &fakeAccountSource{accounts: accounts})
	svc.catalog, svc.channels = catalog, channels
	svc.availability = NewPricingAvailabilityService(repo, nil)
	response, err := svc.BuildForUser(context.Background(), 1, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.ElementsMatch(t, ids[1:], modelIDsOf(response.Models))
	require.Equal(t, 1, catalog.calls, "one metadata snapshot per menu, independent of authorized group count")
	require.Equal(t, 1, channels.calls, "one channel snapshot per menu")
	require.Zero(t, repo.singles, "retirement checks must not issue one SQL read per model/account/group")
	require.LessOrEqual(t, repo.batches, 2, "overlapping group/channel/account models share request-local availability")
	for _, model := range response.Models {
		require.Len(t, model.AuthorizedGroups, len(groups))
	}
	seedAvail(repo.memoryAvailabilityRepo, PlatformOpenAI, ids[2], AvailabilityStatusUnreachable, FailureKindModelNotFound)
	response, err = svc.BuildForUser(context.Background(), 1, MePricingCatalogOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{ids[1]}, modelIDsOf(response.Models), "the next request must see updated retirement evidence")
}

func TestMenuBatchAvailabilityFailureKeepsModelsWithoutQueryStorm(t *testing.T) {
	repo := &countedMenuAvailabilityRepo{memoryAvailabilityRepo: newMemoryRepo(), err: errors.New("availability unavailable")}
	ids := []string{"model-a", "model-b", "model-c"}
	got := tkPruneStructurallyGoneIDs(context.Background(), PlatformOpenAI, ids, NewPricingAvailabilityService(repo, nil))
	require.Equal(t, ids, got, "best-effort retirement filtering must not hide models on dependency failure")
	require.Equal(t, 1, repo.batches)
	require.Zero(t, repo.singles, "a failed batch must not fan out into individual SQL retries")
}

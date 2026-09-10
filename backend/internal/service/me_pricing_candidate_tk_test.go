//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCandidatePricingMenuScopedPriceWithProductionFilter(t *testing.T) {
	const model = "opaque-scoped-model"
	for _, source := range []string{PricingSourceGroup, PricingSourceChannel} {
		for _, universal := range []bool{false, true} {
			t.Run(source+map[bool]string{false: "/direct", true: "/universal"}[universal], func(t *testing.T) {
				groups := []Group{grp(10, PlatformOpenAI, 0, false), grp(20, PlatformOpenAI, 0, false)}
				card := ChannelModelPricing{Platform: PlatformOpenAI, Models: []string{model}, InputPrice: ptrF(.001), OutputPrice: ptrF(.002)}
				if source == PricingSourceGroup {
					groups[0].ModelPricing = []ChannelModelPricing{card}
				}
				account := globalCandidateAccount(1, 1, 10, 20)
				account.Credentials["model_mapping"] = map[string]any{model: model}
				capabilities, key := candidateDiscoveryFixture(groups, []Account{account})
				catalog := NewPricingCatalogService(nil)
				capabilities.modelFilter = NewModelListFilter(catalog, nil)
				capabilities.resolver.candidateGateway.billingService = NewBillingService(nil, nil)
				if source == PricingSourceChannel {
					capabilities.resolver.candidateGateway.channelService = newTestChannelService(makeStandardRepo(Channel{
						ID: 1, Status: StatusActive, GroupIDs: []int64{10}, ModelPricing: []ChannelModelPricing{card},
					}, map[int64]string{10: PlatformOpenAI}))
				}
				if !universal {
					key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &groups[0], &groups[0].ID
				}
				svc := newServiceWithAccounts(&fakeKeyAccess{groups: groups, keys: []APIKey{*key}}, &fakeChannelLister{}, &fakeCatalogProvider{}, &fakeAccountSource{})
				svc.capabilities = capabilities
				response, err := svc.BuildForUser(context.Background(), key.UserID, MePricingCatalogOptions{APIKeyID: &key.ID})
				require.NoError(t, err)
				require.Equal(t, []string{model}, modelIDsOf(response.Models))
				require.Equal(t, 1.0, *response.Models[0].YourPrice.InputPer1K)
				require.Len(t, response.Models[0].AuthorizedGroups, 1, "a scoped price cannot admit another origin")
				require.Equal(t, int64(10), response.Models[0].AuthorizedGroups[0].ID)
				require.False(t, catalog.IsModelPriced(model, PlatformOpenAI), "private prices do not expand the public catalog")
			})
		}
	}
}

func TestCandidatePricingMenuDoesNotRestoreHiddenManifestRows(t *testing.T) {
	const model = "doubao-seed-2.0-code"
	require.True(t, isTkCuratedNewAPIModelListed(model))
	require.False(t, isTkCuratedNewAPIModelDisplayed(model))
	group := grp(10, PlatformOpenAI, 0, false)
	account := globalCandidateAccount(1, 1, 10)
	account.Credentials["model_mapping"] = map[string]any{model: "gpt-5.4"}
	capabilities, key := candidateDiscoveryFixture([]Group{group}, []Account{account})
	catalog := NewPricingCatalogService(nil)
	catalog.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(`{"doubao-seed-2.0-code":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"litellm_provider":"volcengine"}}`), time.Unix(1, 0), true
	})
	capabilities.modelFilter = NewModelListFilter(catalog, nil)
	require.True(t, catalog.IsModelPriced(model, PlatformOpenAI))
	svc := newServiceWithAccounts(&fakeKeyAccess{groups: []Group{group}, keys: []APIKey{*key}}, &fakeChannelLister{}, &fakeCatalogProvider{resp: catalog.BuildPublicCatalog(context.Background())}, &fakeAccountSource{})
	svc.capabilities = capabilities
	response, err := svc.BuildForUser(context.Background(), key.UserID, MePricingCatalogOptions{APIKeyID: &key.ID})
	require.NoError(t, err)
	require.Empty(t, response.Models)
}

func TestCandidatePricingMenuUsesAuthorizedSupport(t *testing.T) {
	for _, universal := range []bool{false, true} {
		group := grp(10, PlatformAnthropic, 0, false)
		account := globalCandidateAccount(1, 1, group.ID)
		// A request alias and a cross-platform membership are intentional boundaries.
		model := "gpt-5.4"
		account.Credentials["model_mapping"] = map[string]any{model: "gpt-5.4-upstream"}
		capabilities, key := candidateDiscoveryFixture([]Group{group}, []Account{account})
		if !universal {
			key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &group, &group.ID
		}
		svc := newServiceWithAccounts(
			&fakeKeyAccess{groups: []Group{group}, keys: []APIKey{*key}},
			&fakeChannelLister{},
			&fakeCatalogProvider{resp: &PublicCatalogResponse{Data: []PublicCatalogModel{mkPublicCatalogModel(model, "openai", 1, 2, 0)}}},
			&fakeAccountSource{},
		)
		svc.capabilities = capabilities
		resp, err := svc.BuildForUser(context.Background(), key.UserID, MePricingCatalogOptions{APIKeyID: &key.ID})
		require.NoError(t, err)
		require.Equal(t, []string{model}, modelIDsOf(resp.Models))
		require.Equal(t, group.ID, resp.Models[0].AuthorizedGroups[0].ID)
		require.Equal(t, 1.0, *resp.Models[0].YourPrice.InputPer1K)
		if universal {
			require.Nil(t, resp.TargetGroup)
			require.Nil(t, key.Group, "metadata cannot bind the execution key")
		}
	}
}

func TestCandidatePricingMenuRejectsPhantomChannelAndConflictingOrigins(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 0, false), grp(20, PlatformOpenAI, 0, false), grp(30, PlatformAnthropic, 0, false)}
	price := 3.0
	groups[1].ModelPricing = []ChannelModelPricing{{Models: []string{"gpt-5.4"}, InputPrice: &price}}
	conflicted := globalCandidateAccount(1, 1, 10, 20)
	peer := globalCandidateAccount(2, 1, 30)
	capabilities, key := candidateDiscoveryFixture(groups, []Account{conflicted, peer})
	svc := newServiceWithAccounts(&fakeKeyAccess{groups: groups, keys: []APIKey{*key}},
		&fakeChannelLister{channels: []AvailableChannel{mkChannelWithModel(1, "phantom", []AvailableGroupRef{{ID: 10}}, []SupportedModel{mkSupportedModel("phantom-only", PlatformOpenAI, mkPricing(1, 2, 0))})}},
		&fakeCatalogProvider{resp: &PublicCatalogResponse{Data: []PublicCatalogModel{mkPublicCatalogModel("gpt-5.4", "openai", 1, 2, 0)}}}, &fakeAccountSource{})
	svc.capabilities = capabilities
	resp, err := svc.BuildForUser(context.Background(), key.UserID, MePricingCatalogOptions{APIKeyID: &key.ID})
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-5.4"}, modelIDsOf(resp.Models))
	require.Len(t, resp.Models[0].AuthorizedGroups, 1)
	require.Equal(t, int64(30), resp.Models[0].AuthorizedGroups[0].ID)
}

type pricingMenuAvailabilityFixture struct {
	state AvailabilityState
}

func (f pricingMenuAvailabilityFixture) GetAvailability(context.Context, string, string) (AvailabilityState, error) {
	return f.state, nil
}

func TestCandidatePricingMenuPrunesEveryPriceSource(t *testing.T) {
	model := firstNPlatformModelIDsForMePricingTest(t, PlatformOpenAI, 1)[0]
	group := mkGroupForMe(10, "authorized", PlatformOpenAI, 1)
	for _, channel := range []bool{false, true} {
		for _, gone := range []bool{false, true} {
			channels := &fakeChannelLister{}
			accounts := &fakeAccountSource{}
			if channel {
				channels.channels = []AvailableChannel{mkChannelWithModel(1, "priced", []AvailableGroupRef{{ID: 10}}, []SupportedModel{mkSupportedModel(model, PlatformOpenAI, mkPricing(1, 2, 0))})}
			} else {
				accounts.accounts = []Account{mkAccountWithWhitelist(1, "member", PlatformOpenAI, 0, []string{model})}
			}
			svc := newServiceWithAccounts(&fakeKeyAccess{groups: []Group{group}}, channels,
				&fakeCatalogProvider{resp: &PublicCatalogResponse{Data: []PublicCatalogModel{mkPublicCatalogModel(model, "openai", 1, 2, 0)}}}, accounts)
			state := AvailabilityState{Status: AvailabilityStatusUnreachable}
			if gone {
				state.LastFailureKind = FailureKindModelNotFound
			}
			svc.availability = pricingMenuAvailabilityFixture{state: state}
			resp, err := svc.BuildForUser(context.Background(), 7, MePricingCatalogOptions{})
			require.NoError(t, err)
			if gone {
				require.Empty(t, resp.Models)
			} else {
				require.Equal(t, []string{model}, modelIDsOf(resp.Models), "transient evidence cannot hide a model")
			}
		}
	}
}

//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPricingNamespaceCatalogBillingParity(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	catalog := NewPricingCatalogService(nil)
	billing := activeRegistryBillingService(t)
	snapshot := loadTKPricingOverlaySnapshot()
	// Derive decimal owner and alias cases from the registry; names are not a second catalog.
	owners := map[string]string{}
	for id, price := range snapshot.Models {
		if strings.Contains(id, ".") && !price.TokenPricingAbsent && price.InputCostPerToken > 0 {
			owners[id] = id
		}
	}
	for alias, owner := range snapshot.Aliases {
		if price := snapshot.Models[owner]; strings.Contains(alias, ".") && price != nil && !price.TokenPricingAbsent && price.InputCostPerToken > 0 {
			owners[alias] = owner
		}
	}
	for id, owner := range owners {
		if !catalog.IsModelPriced(id, "") {
			continue
		}
		t.Run(id, func(t *testing.T) {
			qualified := "vendor/" + id
			row, ok := catalog.findCatalogModel(qualified)
			require.True(t, ok)
			require.Equal(t, owner, row.ModelID)
			price, err := billing.GetModelPricing(qualified)
			require.NoError(t, err)
			require.InDelta(t, price.InputPricePerToken*1000, row.Pricing.InputPer1KTokens, 1e-12)
			require.Equal(t, []string{qualified}, NewModelListFilter(catalog, nil).FilterClientFacing(context.Background(), PlatformNewAPI, []string{qualified}))
		})
	}
	// Unknown families and ambiguous namespaces cannot inherit a known model's price membership.
	for _, id := range []string{"vendor/unknown-9.9", "vendor/gpt", "foo/bar/gpt-5.4", "/gpt-5.4", "vendor/"} {
		require.False(t, catalog.IsModelPriced(id, ""), id)
	}
}

func TestPricingNamespaceMenuFollowsRegistryAliasRotation(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	// Fixed spelling boundaries from the incident, including an alias with no standalone price row.
	models := []string{"openai/gpt-5.4", "openai/gpt-5.6"}
	group := grp(10, PlatformAnthropic, 0, false)
	account := globalCandidateAccount(1, 1, group.ID)
	account.Credentials["model_mapping"] = map[string]any{models[0]: "gpt-5.4", models[1]: "gpt-5.6-sol"}
	capabilities, key := candidateDiscoveryFixture([]Group{group}, []Account{account})
	catalog := NewPricingCatalogService(nil)
	capabilities.modelFilter = NewModelListFilter(catalog, nil)
	capabilities.resolver.candidateGateway.billingService = activeRegistryBillingService(t)
	svc := &MePricingCatalogService{keys: &fakeKeyAccess{groups: []Group{group}, keys: []APIKey{*key}}, channels: &fakeChannelLister{}, catalog: catalog, accounts: &fakeAccountSource{}}
	svc.capabilities = capabilities
	check := func() {
		t.Helper()
		for _, mode := range []string{RoutingModeDirect, RoutingModeUniversal} {
			key.RoutingMode = mode
			if mode == RoutingModeDirect {
				key.Group, key.GroupID = &group, &group.ID
			} else {
				key.Group, key.GroupID = nil, nil
			}
			svc.keys = &fakeKeyAccess{groups: []Group{group}, keys: []APIKey{*key}}
			response, err := svc.BuildForUser(context.Background(), key.UserID, MePricingCatalogOptions{APIKeyID: &key.ID})
			require.NoError(t, err)
			require.ElementsMatch(t, models, modelIDsOf(response.Models))
			for _, row := range response.Models {
				price, err := capabilities.resolver.candidateGateway.billingService.GetModelPricing(row.ModelID)
				require.NoError(t, err)
				require.NotNil(t, row.YourPrice.InputPer1K, row.ModelID)
				require.InDelta(t, price.InputPricePerToken*1000, *row.YourPrice.InputPer1K, 1e-12, row.ModelID)
			}
		}
	}
	check()
	envelope := registryEnvelopeForTest(t, func(registry map[string]any) {
		registry["_aliases"].(map[string]any)["gpt-5.6"] = "gpt-5.4"
	}, nil)
	rebuildTKOverlayUnion([]byte(envelope))
	check()
}

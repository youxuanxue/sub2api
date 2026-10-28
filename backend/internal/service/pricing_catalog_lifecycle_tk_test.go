package service

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func recommendedModelIDsForTest(ids []string) []string {
	var out []string
	for _, id := range ids {
		if isCatalogModelRecommended(id) {
			out = append(out, id)
		}
	}
	return out
}

func TestCatalogLifecycleWithdrawalFacts(t *testing.T) {
	seen := make(map[string]bool)
	for _, withdrawal := range catalogModelWithdrawals {
		source, err := url.Parse(withdrawal.Source)
		require.NoError(t, err)
		require.Equal(t, "https", source.Scheme)
		require.NotEmpty(t, source.Host)
		_, err = time.Parse(time.DateOnly, withdrawal.Sunset)
		require.NoError(t, err)
		for _, id := range withdrawal.ModelIDs {
			require.False(t, seen[id], "duplicate lifecycle owner for %s", id)
			seen[id] = true
			require.False(t, isCatalogModelRecommended(id))
			require.False(t, isCatalogModelRecommended("vendor/"+id))
		}
	}
	for _, id := range []string{"glm-5.2", "qwen-plus", "qwen3.7-plus", "qwen3.6-flash", "gemini-3.1-flash-image", "claude-sonnet-4-5", "claude-haiku-4-5"} {
		require.True(t, isCatalogModelRecommended(id), "active replacement or stable ID %s must remain eligible", id)
	}
	// Operator-hidden while sole Grok account is unschedulable (see catalogWithdrawnModelIDs).
	require.False(t, isCatalogModelRecommended("grok-4.5"))
	require.False(t, isCatalogModelRecommended("grok-imagine-image"))
	// Unsourced NewAPI long-tail: display=false in tk_served_models.json.
	// glm-5.1 and kimi-k2.6 were sole-supplied by the Baidu Qianfan Token Plan.
	// That subscription was not renewed (operator decision 2026-10-25), so both
	// lose display: kimi-k2.6 already had Baidu's own sunset notice above, while
	// glm-5.1 has no upstream announcement and therefore belongs here, not in
	// catalogModelWithdrawals (whose Source must be a published notice).
	// glm-4.7/glm-4.5-air are withdrawn from the Qianfan Token Plan mapping.
	require.False(t, isTkCuratedNewAPIModelDisplayed("glm-5.1"))
	require.False(t, isTkCuratedNewAPIModelDisplayed("kimi-k2.6"))
	// glm-5.2 stays mapped and priced. Public display is off; glm-5.3 is the advertised generation.
	require.True(t, isCatalogModelRecommended("glm-5.2"))
	require.False(t, isTkCuratedNewAPIModelDisplayed("glm-5.2"))
	require.False(t, isTkCuratedNewAPIModelDisplayed("deepseek-v4-flash-0731"))
	require.False(t, isTkCuratedNewAPIModelDisplayed("kimi-k2.5"))
	require.False(t, isTkCuratedNewAPIModelDisplayed("moonshot-v1-128k"))
	require.False(t, isTkCuratedNewAPIModelDisplayed("doubao-seedream-5-0-260128"))
	require.True(t, isTkCuratedNewAPIModelDisplayed("doubao-seedream-5-0-pro"))
}

func TestManifestDisplayAgreesWithWithdrawal(t *testing.T) {
	var doc tkServedModelsManifestFile
	require.NoError(t, json.Unmarshal(tkServedModelsManifestRaw, &doc))
	for id, entry := range doc.Entries {
		if _, withdrawn := catalogWithdrawnModelIDs[id]; !withdrawn || !isTkCuratedNewAPIModelListed(id) {
			continue
		}
		require.False(t, entry.Display, "withdrawn curated model %s keeps display=false in the manifest", id)
		require.False(t, isTkCuratedNewAPIModelDisplayed(id), id)
	}
}

func TestCatalogLifecyclePublicFilterKeepsCachedPricing(t *testing.T) {
	full := &PublicCatalogResponse{Data: []PublicCatalogModel{{ModelID: "qwen3.7-plus", Vendor: "dashscope"}}}
	for id := range catalogWithdrawnModelIDs {
		for _, vendor := range []string{"dashscope", "openai", "anthropic", "antigravity", "vertex_ai-language-models"} {
			full.Data = append(full.Data, PublicCatalogModel{ModelID: id, Vendor: vendor})
		}
	}
	before := append([]PublicCatalogModel(nil), full.Data...)
	public := FilterPublicCatalogToServable(full)
	require.Len(t, public.Data, 1)
	require.Equal(t, "qwen3.7-plus", public.Data[0].ModelID)
	require.Equal(t, before, full.Data, "recommendation filtering must not mutate cached pricing")
}

func TestCatalogLifecycleNewAPIProvisioningSurvivesWithdrawal(t *testing.T) {
	for _, channelType := range NewAPIManifestPresetChannelTypes() {
		account := &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: channelType}
		mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
		require.True(t, ok)
		for _, id := range tkServedModelsManifestPresetIDsByChannelType(channelType) {
			require.Equal(t, id, mapping[id], "provisioning retains %s on channel %d", id, channelType)
			if !isCatalogModelRecommended(id) {
				require.True(t, isTkCuratedNewAPIModelListed(id))
				require.NotContains(t, NewAPIModelDisplayIDsForAccount(account), id)
			}
		}
	}
}

func TestCatalogLifecycleWithdrawnNewAPIModelsKeepSettlementPricing(t *testing.T) {
	pricing := &PricingCatalogService{}
	for id := range catalogWithdrawnModelIDs {
		if isTkCuratedNewAPIModelListed(id) {
			require.True(t, pricing.IsModelPriced(id, PlatformNewAPI), "withdrawal must keep %s priced", id)
		}
	}
}

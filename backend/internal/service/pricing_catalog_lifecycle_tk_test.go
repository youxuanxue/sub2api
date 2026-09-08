package service

import (
	"context"
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
	pricing.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(`{"gpt-5.5":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"litellm_provider":"openai"}}`), time.Now(), true
	})
	for id := range catalogWithdrawnModelIDs {
		if isTkCuratedNewAPIModelListed(id) {
			require.True(t, pricing.IsModelPriced(id, PlatformNewAPI), "withdrawal must keep %s priced", id)
		}
	}
}

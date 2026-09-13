//go:build unit

package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestPricingSSOTRegistryOwnsCatalogWithoutSensors(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	cfg := &config.Config{}
	cfg.Pricing.DataDir = t.TempDir()
	cfg.Pricing.FallbackFile = filepath.Join(cfg.Pricing.DataDir, "fallback.json")
	catalog := NewPricingCatalogService(cfg)
	first := catalog.BuildPublicCatalog(context.Background())
	require.NotEmpty(t, first.Data)
	for _, content := range []string{"", "not-json", `{"legacy-only":{"input_cost_per_token":0.9,"litellm_provider":"openai"}}`} {
		require.NoError(t, os.WriteFile(filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"), []byte(content), 0600))
		require.NoError(t, os.WriteFile(cfg.Pricing.FallbackFile, []byte(content), 0600))
		require.Same(t, first, catalog.BuildPublicCatalog(context.Background()))
		require.False(t, catalog.IsModelPriced("legacy-only", PlatformOpenAI))
	}
}

func TestPricingSSOTRegistryDeletionRotatesCatalogAliasesAndBilling(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	const model = "qwen3.7-plus"
	snapshot := loadTKPricingOverlaySnapshot()
	require.NotNil(t, snapshot.Models[model])
	source, err := json.Marshal(map[string]*LiteLLMModelPricing{model: snapshot.Models[model]})
	require.NoError(t, err)
	cfg := &config.Config{}
	cfg.Pricing.DataDir = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Pricing.DataDir, "model_pricing.json"), source, 0600))
	catalog := NewPricingCatalogService(cfg)
	billing := activeRegistryBillingService(t)
	require.True(t, catalog.IsModelPriced(model, PlatformNewAPI))
	for alias, owner := range snapshot.Aliases {
		if _, ok := catalog.findCatalogModel(owner); ok {
			require.True(t, catalog.IsModelPriced(alias, ""), alias)
		}
	}
	old := catalog.BuildPublicCatalog(context.Background())
	envelope := registryEnvelopeForTest(t, func(registry map[string]any) {
		delete(registry, model)
		aliases := registry["_aliases"].(map[string]any)
		for alias, target := range aliases {
			if target == model {
				delete(aliases, alias)
			}
		}
	}, nil)
	rebuildTKOverlayUnion([]byte(envelope))
	require.False(t, catalog.IsModelPriced(model, PlatformNewAPI))
	require.NotSame(t, old, catalog.BuildPublicCatalog(context.Background()))
	_, err = billing.GetModelPricing(model)
	require.Error(t, err)
	for _, row := range FilterPublicCatalogToServable(catalog.BuildPublicCatalog(context.Background())).Data {
		require.NotEqual(t, model, row.ModelID)
	}
	for alias, owner := range snapshot.Aliases {
		if owner == model {
			require.False(t, catalog.IsModelPriced(alias, ""))
		}
	}
}

func TestPricingSSOTSingleAccountFailureCannotRetireModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"missing", 404, "model not found"},
		{"codex_account_restriction", 400, "The model is not supported when using Codex with a ChatGPT account"},
		{"auth", 401, "unauthorized"}, {"quota", 429, "quota exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, _, _ := newAvailabilityTestService(t)
			for i := 0; i < 20; i++ {
				svc.RecordOutcome(ctx, AvailabilityOutcome{Platform: PlatformOpenAI, ModelID: "gpt-5.4", AccountID: 101, Success: true, UpstreamStatusCode: 200})
			}
			for i := 0; i < 25; i++ {
				svc.RecordOutcome(ctx, AvailabilityOutcome{Platform: PlatformOpenAI, ModelID: "gpt-5.4", AccountID: 202, UpstreamStatusCode: tc.status, UpstreamErrorBody: tc.body, ProviderModelRetired: true})
				require.False(t, svc.IsStructurallyGone(ctx, PlatformOpenAI, "gpt-5.4"))
				public := DecorateAndPruneByAvailability(ctx, &PublicCatalogResponse{Data: []PublicCatalogModel{{ModelID: "gpt-5.4", Vendor: "openai"}}}, svc)
				require.Len(t, public.Data, 1)
			}
		})
	}
}

func TestPricingSSOTProviderRetirementRequiresScopedRepeatedEvidence(t *testing.T) {
	ctx := context.Background()
	svc, repo, clk := newAvailabilityTestService(t)
	outcome := AvailabilityOutcome{Platform: PlatformOpenAI, ModelID: "retired-model", UpstreamStatusCode: 404, UpstreamErrorBody: "model not found"}
	svc.RecordOutcome(ctx, outcome)
	require.False(t, svc.IsStructurallyGone(ctx, PlatformOpenAI, outcome.ModelID), "account-less does not mean provider-wide")
	outcome.ProviderModelRetired = true
	svc.RecordOutcome(ctx, outcome)
	require.False(t, svc.IsStructurallyGone(ctx, PlatformOpenAI, outcome.ModelID), "legacy evidence cannot supply confirmation")
	svc.RecordOutcome(ctx, outcome)
	require.False(t, svc.IsStructurallyGone(ctx, PlatformOpenAI, outcome.ModelID), "duplicate timestamp is not confirmation")
	clk.Advance(time.Minute)
	svc.RecordOutcome(ctx, outcome)
	require.True(t, svc.IsStructurallyGone(ctx, PlatformOpenAI, outcome.ModelID))
	svc.RecordOutcome(ctx, outcome)
	require.True(t, svc.IsStructurallyGone(ctx, PlatformOpenAI, outcome.ModelID), "a duplicate cannot revoke an already confirmed retirement")
	public := DecorateAndPruneByAvailability(ctx, &PublicCatalogResponse{Data: []PublicCatalogModel{{ModelID: outcome.ModelID, Vendor: "openai"}}}, svc)
	require.Empty(t, public.Data)
	svc.RecordOutcome(ctx, AvailabilityOutcome{Platform: PlatformOpenAI, ModelID: outcome.ModelID, AccountID: 101, Success: true, UpstreamStatusCode: 200})
	require.False(t, svc.IsStructurallyGone(ctx, PlatformOpenAI, outcome.ModelID), "success revokes retirement")
	clk.Advance(AvailabilityRollingWindow)
	svc.RecordOutcome(ctx, outcome)
	require.False(t, svc.IsStructurallyGone(ctx, PlatformOpenAI, outcome.ModelID), "expired confirmation cannot retire a model")
	require.NoError(t, repo.Upsert(ctx, PlatformOpenAI, outcome.ModelID, func(s AvailabilityState) AvailabilityState {
		s.Status = AvailabilityStatusUnreachable
		s.LastFailureKind = FailureKindModelNotFound
		return s
	}))
	require.False(t, svc.IsStructurallyGone(ctx, PlatformOpenAI, outcome.ModelID), "old persisted 404 rows fail open without a schema migration")
}

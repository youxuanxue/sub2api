//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVolcEnginePlanAudioCandidateAuthorizationAndCooldown(t *testing.T) {
	for _, direct := range []bool{false, true} {
		groups := []Group{grp(10, PlatformNewAPI, 1, false)}
		makeAccount := func(id int64, group int64) Account {
			a := *volcEnginePlanTestAccount()
			a.ID, a.GroupIDs, a.Status, a.Schedulable, a.Concurrency = id, []int64{group}, StatusActive, true, 10
			a.Credentials["model_mapping"] = map[string]any{VolcEnginePlanASRModel: VolcEnginePlanASRModel}
			return a
		}
		ready, cooling, unauthorized := makeAccount(1, 10), makeAccount(2, 10), makeAccount(3, 20)
		until := time.Now().Add(time.Hour)
		cooling.RateLimitResetAt = &until
		ready.Priority, cooling.Priority, unauthorized.Priority = 10, 1, 0
		r, _, key := globalCandidateFixture(groups, []Account{ready, cooling, unauthorized})
		if direct {
			key.RoutingMode = RoutingModeDirect
			key.Group = &groups[0]
			key.GroupID = &groups[0].ID
		}
		ctx, request, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIAudioTranscription, "/v1/audio/transcriptions", VolcEnginePlanASRModel, nil, "", "")
		require.NoError(t, err)
		require.NotNil(t, request)
		require.Equal(t, int64(1), request.current.account.ID)
		_, governed := ProtocolRoutingRequest(ctx)
		require.False(t, governed)
		require.Equal(t, int64(10), *key.GroupID)
	}
}

func TestVolcEnginePlanAudioShapeAndWrongProvider(t *testing.T) {
	require.Equal(t, ShapeOpenAIAudioTranscription, UniversalShapeForRequest("/v1/audio/transcriptions", "POST"))
	require.Equal(t, ShapeOpenAIAudioSpeech, UniversalShapeForRequest("/audio/speech", "POST"))
	account := volcEnginePlanTestAccount()
	require.True(t, universalOpenAICompatAccountSupportsShape(account, ShapeOpenAIAudioTranscription))
	account.Credentials["base_url"] = "https://ark.cn-beijing.volces.com/api/v3"
	require.False(t, universalOpenAICompatAccountSupportsShape(account, ShapeOpenAIAudioTranscription))
	account = volcEnginePlanTestAccount()
	account.Credentials["model_mapping"] = map[string]any{VolcEnginePlanASRModel: "wrong-model"}
	require.False(t, universalOpenAICompatAccountSupportsModel(context.Background(), nil, account, VolcEnginePlanASRModel, ShapeOpenAIAudioTranscription))
}

func TestVolcEnginePlanASRHoldMatchesDurationSettlement(t *testing.T) {
	repo := &videoHoldRepoStub{}
	billing := NewBillingService(nil, nil)
	s := &OpenAIGatewayService{billingService: billing, usageBillingRepo: repo}
	held, reject := s.TkReserveSTTHold(context.Background(), "asr-hold", VolcEnginePlanASRModel, &User{ID: 1}, &APIKey{ID: 2}, 2.998)
	require.True(t, held)
	require.False(t, reject)
	cost := billing.CalculateAudioCostForModel(VolcEnginePlanASRModel, "stt", 2998.0/3_600_000, nil, 1)
	require.InDelta(t, cost.ActualCost, repo.command.Amount, 1e-12)
}

func TestVolcEnginePlanAudioCatalogUsesSettlementUnits(t *testing.T) {
	catalog := &PricingCatalogService{}
	response := catalog.BuildPublicCatalog(context.Background())
	found := false
	for _, model := range response.Data {
		if model.ModelID != VolcEnginePlanASRModel {
			continue
		}
		found = true
		require.Equal(t, "stt", model.Pricing.BillingMode)
		require.InDelta(t, 1.0/6.7/3600*1.06, model.Pricing.InputCostPerSecond, 1e-12)
		var entry MePricingModel
		applyCatalogMetaToMePricingModel(&entry, model, 1)
		require.Equal(t, "stt", entry.BillingMode)
		require.NotNil(t, entry.YourPrice.PerInputSecond)
		require.Equal(t, model.Pricing.InputCostPerSecond, *entry.YourPrice.PerInputSecond)
	}
	require.True(t, found)
}

func TestVolcEnginePlanEmbeddingCatalogPreservesBothInputPrices(t *testing.T) {
	catalog := &PricingCatalogService{}
	for _, model := range catalog.BuildPublicCatalog(context.Background()).Data {
		if model.ModelID != "doubao-embedding-vision" {
			continue
		}
		require.Equal(t, "embedding", model.Pricing.BillingMode)
		require.InDelta(t, 0.7/6.7/1000*1.06, model.Pricing.InputPer1KTokens, 1e-15)
		require.InDelta(t, 1.8/6.7/1_000_000*1.06, model.Pricing.InputCostPerImageToken, 1e-15)
		require.Contains(t, model.Capabilities, "vision")
		entry := buildAccountFallbackEntry(model.ModelID, 1, map[string]PublicCatalogModel{model.ModelID: model})
		require.NotNil(t, entry.YourPrice.PerImageInputToken)
		require.Equal(t, model.Pricing.InputCostPerImageToken, *entry.YourPrice.PerImageInputToken)
		return
	}
	t.Fatal("reviewed embedding model missing from public catalog")
}

//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

// These are the reported compatibility boundaries, not a copy of the model catalog.
func TestCandidateEligibilityGeminiNativeModelAdmission(t *testing.T) {
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	for _, model := range []string{"gemini-3-flash-preview", "gemini-3.5-flash-lite"} {
		for _, inbound := range []struct {
			name, path, body string
			shape            UniversalShape
		}{
			{"native", "/v1beta/models/" + model + ":generateContent", `{"contents":[{"role":"user","parts":[{"text":"OK"}]}]}`, ShapeGemini},
			{"native_stream", "/v1beta/models/" + model + ":streamGenerateContent", `{"contents":[{"role":"user","parts":[{"text":"OK"}]}]}`, ShapeGemini},
			{"chat_converter", "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"OK"}]}`, model), ShapeOpenAIChat},
			{"chat_converter_stream", "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"OK"}]}`, model), ShapeOpenAIChat},
		} {
			t.Run(model+"/"+inbound.name, func(t *testing.T) {
				resolver, gateway, accounts, _ := candidateGoogleFixture(t)
				accounts[0].Credentials["model_mapping"] = modelMappingToAny(floor.NewAPIChannelTypes["41"])
				accounts[1].Credentials["model_mapping"] = modelMappingToAny(floor.Platforms[PlatformAntigravity])
				wireCandidateTestResolver(resolver, gateway, accounts)
				for i := range accounts {
					groupID := accounts[i].GroupIDs[0]
					discovery := &GatewayService{accountRepo: &modelsListAccountRepoStub{
						byGroup: map[int64][]Account{groupID: {accounts[i]}},
					}}
					ids, _, err := discovery.GetAvailableModelsForDiscovery(context.Background(), groupID, accounts[i].Platform)
					require.NoError(t, err)
					require.Contains(t, ids, model, "discovery and candidate evaluation must share the admitted model")
				}
				ctx := resolver.WithRequest(context.Background(), inbound.shape, inbound.path, model, []byte(inbound.body))
				for i := range accounts {
					plan, governed, err := protocolPlanForAccount(ctx, &accounts[i], model)
					require.True(t, governed)
					require.NoError(t, err, "account %d", accounts[i].ID)
					require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, plan.TargetProtocol())
					if i == 0 {
						want := map[string]string{
							"gemini-3-flash-preview": "gemini-3.8-flash",
							"gemini-3.5-flash-lite":  "gemini-3.6-flash",
						}[model]
						require.Equal(t, want, plan.ResolvedModel())
						require.Contains(t, plan.Endpoint(), "/locations/global/")
					} else {
						want := map[string]string{
							"gemini-3-flash-preview": "gemini-3.8-flash-high",
							"gemini-3.5-flash-lite":  "gemini-3.6-flash-tiered",
						}[model]
						require.Equal(t, want, plan.ResolvedModel())
					}
				}
				group, err := resolver.Resolve(ctx, universalKey(334), inbound.shape, model, "")
				require.NoError(t, err)
				require.Equal(t, int64(16), group.ID)
				accounts[0].Schedulable = false
				wireCandidateTestResolver(resolver, gateway, accounts)
				group, err = resolver.Resolve(ctx, universalKey(334), inbound.shape, model, "")
				require.NoError(t, err)
				require.Equal(t, int64(21), group.ID, "Antigravity remains a legal fallback")
				accounts[1].Schedulable = false
				wireCandidateTestResolver(resolver, gateway, accounts)
				_, err = resolver.Resolve(ctx, universalKey(334), inbound.shape, model, "")
				require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
			})
		}
	}
}

func TestCandidateEligibilityGeminiNativeModelPricing(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	pricing := &PricingService{useActiveRegistry: true}
	for _, model := range []string{"gemini-3-flash-preview", "gemini-3.5-flash-lite"} {
		entry := pricing.GetModelPricing(model)
		require.NotNil(t, entry)
		require.False(t, tkIsEffectivelyUnpriced(entry))
		require.True(t, isPublicCatalogModelSupported("gemini", model))
		require.True(t, isPublicCatalogModelSupported(PlatformAntigravity, model))
		require.Equal(t, "gemini", presentationVendorForServable(model, "gemini"))
		require.Contains(t, NewAPIModelDisplayIDsForChannelType(newapiconstant.ChannelTypeVertexAi), model)
	}
	// Compatibility aliases must bill at the remapped target list price.
	preview, wire38 := pricing.GetModelPricing("gemini-3-flash-preview"), pricing.GetModelPricing("gemini-3.8-flash")
	require.NotNil(t, wire38)
	require.Equal(t, wire38.InputCostPerToken, preview.InputCostPerToken)
	require.Equal(t, wire38.OutputCostPerToken, preview.OutputCostPerToken)
	require.Equal(t, wire38.CacheReadInputTokenCost, preview.CacheReadInputTokenCost)
	lite, wire36 := pricing.GetModelPricing("gemini-3.5-flash-lite"), pricing.GetModelPricing("gemini-3.6-flash")
	require.NotNil(t, wire36)
	require.Equal(t, wire36.InputCostPerToken, lite.InputCostPerToken)
	require.Equal(t, wire36.OutputCostPerToken, lite.OutputCostPerToken)
	require.Equal(t, wire36.CacheReadInputTokenCost, lite.CacheReadInputTokenCost)
}

func TestCandidateEligibilityGeminiNativeActivationScope(t *testing.T) {
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	wantVertex := map[string]string{
		"gemini-3-flash-preview": "gemini-3.8-flash",
		"gemini-3.5-flash-lite":  "gemini-3.6-flash",
	}
	for _, model := range []string{"gemini-3-flash-preview", "gemini-3.5-flash-lite"} {
		require.Equal(t, wantVertex[model], floor.NewAPIChannelTypes["41"][model])
		for profile, mapping := range floor.VertexCapabilityProfiles {
			require.Equal(t, wantVertex[model], mapping[model], "profile %s", profile)
		}
		require.NotEmpty(t, floor.Platforms[PlatformAntigravity][model])
		// Converged Google text aliases are also on the Gemini floor (traffic aliases).
		require.Equal(t, wantVertex[model], floor.Platforms[PlatformGemini][model])
		require.Contains(t, AccountModelMappingPresetIDs(context.Background(), PlatformGemini, 0, nil), model)
	}
}

func TestCandidateEligibilityGeminiNativeEdgeMapping(t *testing.T) {
	for _, model := range []string{"gemini-3-flash-preview", "gemini-3.5-flash-lite"} {
		t.Run(model, func(t *testing.T) {
			resolver, _, _, _ := candidateGoogleFixture(t)
			account := &Account{ID: 5, Platform: PlatformAntigravity, Type: AccountTypeOAuth,
				Credentials: map[string]any{"project_id": "test-project", "access_token": "test-only"}}
			attachTestProtocolCapability(account, protocolrouter.ProtocolGeminiGenerateContent)
			ctx := resolver.WithRequest(context.Background(), ShapeGemini, "/v1beta/models/"+model+":generateContent", model,
				[]byte(`{"contents":[{"role":"user","parts":[{"text":"OK"}]}]}`))
			plan, governed, err := protocolPlanForAccount(ctx, account, model)
			require.True(t, governed)
			require.NoError(t, err)
			require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, plan.TargetProtocol())
			require.Equal(t, MapAntigravityModel(account, model), plan.ResolvedModel())
			require.Empty(t, MapAntigravityModel(account, "gemini-unknown-preview"), "unknown preview names must not gain generic alias support")
		})
	}
}

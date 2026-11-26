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
	for _, model := range []string{"gemini-3-flash"} {
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
						require.Equal(t, "gemini-3-flash", plan.ResolvedModel())
						if vertexModelUsesGlobalLocation(plan.ResolvedModel()) {
							require.Contains(t, plan.Endpoint(), "/locations/global/")
						} else {
							require.NotContains(t, plan.Endpoint(), "/locations/global/")
						}
					} else {
						require.Equal(t, "gemini-3-flash", plan.ResolvedModel())
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
				require.ErrorIs(t, err, ErrUniversalNoEntitledGroup)
				require.NotErrorIs(t, err, ErrUniversalCapacityUnavailable)
			})
		}
	}
}

func TestCandidateEligibilityGeminiNativeModelPricing(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	pricing := &PricingService{useActiveRegistry: true}
	for _, model := range []string{"gemini-3-flash"} {
		entry := pricing.GetModelPricing(model)
		require.NotNil(t, entry)
		require.False(t, tkIsEffectivelyUnpriced(entry))
		require.True(t, isPublicCatalogModelSupported("gemini", model))
		require.Equal(t, "gemini", presentationVendorForServable(model, "gemini"))
		require.Contains(t, NewAPIModelDisplayIDsForChannelType(newapiconstant.ChannelTypeVertexAi), model)
	}
	for _, withdrawn := range []string{"gemini-3-flash-preview", "gemini-3.7-flash", "veo-3.1-generate-001"} {
		require.False(t, isPublicCatalogModelSupported("gemini", withdrawn))
		require.False(t, isPublicCatalogModelSupported(PlatformAntigravity, withdrawn))
	}
	for _, retired := range []string{"gemini-3.6-flash", "gemini-3.5-flash-lite"} {
		require.False(t, isPublicCatalogModelSupported("gemini", retired))
		require.False(t, isPublicCatalogModelSupported(PlatformAntigravity, retired))
	}
}

func TestCandidateEligibilityGeminiNativeActivationScope(t *testing.T) {
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	const model = "gemini-3-flash"
	require.Equal(t, model, floor.NewAPIChannelTypes["41"][model])
	for profile, mapping := range floor.VertexCapabilityProfiles {
		require.Equal(t, model, mapping[model], "profile %s", profile)
	}
	require.Equal(t, model, floor.Platforms[PlatformAntigravity][model])
	require.Equal(t, model, floor.Platforms[PlatformGemini][model])
	require.Contains(t, AccountModelMappingPresetIDs(context.Background(), PlatformGemini, 0, nil), model)
	require.NotContains(t, floor.NewAPIChannelTypes["41"], "gemini-3-flash-preview")
}

func TestCandidateEligibilityGeminiNativeEdgeMapping(t *testing.T) {
	for _, model := range []string{"gemini-3-flash", "gemini-3.5-flash-lite", "gemini-3-flash-preview"} {
		t.Run(model, func(t *testing.T) {
			resolver, _, _, _ := candidateGoogleFixture(t)
			account := &Account{ID: 5, Platform: PlatformAntigravity, Type: AccountTypeOAuth,
				Credentials: map[string]any{"project_id": "test-project", "access_token": "test-only"}}
			attachTestProtocolCapability(account, protocolrouter.ProtocolGeminiGenerateContent)
			ctx := resolver.WithRequest(context.Background(), ShapeGemini, "/v1beta/models/"+model+":generateContent", model,
				[]byte(`{"contents":[{"role":"user","parts":[{"text":"OK"}]}]}`))
			plan, governed, err := protocolPlanForAccount(ctx, account, model)
			require.True(t, governed)
			switch model {
			case "gemini-3.5-flash-lite":
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, plan.TargetProtocol())
			require.Equal(t, MapAntigravityModel(account, model), plan.ResolvedModel())
			require.Empty(t, MapAntigravityModel(account, "gemini-unknown-preview"), "unknown preview names must not gain generic alias support")
		})
	}
}

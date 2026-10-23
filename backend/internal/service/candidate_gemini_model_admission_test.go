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
	// 2026-10-23: 原先还有一个 gemini-3.5-flash-lite 样本，测「别名 → 3.6-flash」。
	// 两者已整体退役，而 gemini-only 的其余模型都是 embedding/image/video，不走
	// generateContent，不存在同形替代，故该槽位随退役移除。
	for _, model := range []string{"gemini-3-flash-preview"} {
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
						}[model]
						require.Equal(t, want, plan.ResolvedModel())
						require.Contains(t, plan.Endpoint(), "/locations/global/")
					} else {
						require.Equal(t, "gemini-3.8-flash-high", plan.ResolvedModel())
					}
				}
				group, err := resolver.Resolve(ctx, universalKey(334), inbound.shape, model, "")
				require.NoError(t, err)
				require.Equal(t, int64(16), group.ID)
				accounts[0].Schedulable = false
				wireCandidateTestResolver(resolver, gateway, accounts)
				group, err = resolver.Resolve(ctx, universalKey(334), inbound.shape, model, "")
				if model == "gemini-3.5-flash-lite" {
					require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
				} else {
					require.NoError(t, err)
					require.Equal(t, int64(21), group.ID, "Antigravity remains a legal fallback")
				}
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
	// 2026-10-23: 样本从 3.5-flash-lite 换成 gemini-3-flash —— 前者与其上游
	// 3.6-flash 已整体退役。这里验证的是「兼容别名按重映射目标计价」这个机制，
	// 不是具体模型清单，所以换一个仍在供应的别名即可。
	for _, model := range []string{"gemini-3-flash-preview", "gemini-3-flash"} {
		entry := pricing.GetModelPricing(model)
		require.NotNil(t, entry)
		require.False(t, tkIsEffectivelyUnpriced(entry))
		require.True(t, isPublicCatalogModelSupported("gemini", model))
		require.Equal(t, "gemini", presentationVendorForServable(model, "gemini"))
		require.Contains(t, NewAPIModelDisplayIDsForChannelType(newapiconstant.ChannelTypeVertexAi), model)
	}
	// Compatibility aliases must bill at the remapped target list price.
	preview, wire38 := pricing.GetModelPricing("gemini-3-flash-preview"), pricing.GetModelPricing("gemini-3.8-flash")
	require.NotNil(t, wire38)
	require.Equal(t, wire38.InputCostPerToken, preview.InputCostPerToken)
	require.Equal(t, wire38.OutputCostPerToken, preview.OutputCostPerToken)
	require.Equal(t, wire38.CacheReadInputTokenCost, preview.CacheReadInputTokenCost)
	bare := pricing.GetModelPricing("gemini-3-flash")
	require.NotNil(t, bare)
	require.Equal(t, wire38.InputCostPerToken, bare.InputCostPerToken)
	require.Equal(t, wire38.OutputCostPerToken, bare.OutputCostPerToken)
	require.Equal(t, wire38.CacheReadInputTokenCost, bare.CacheReadInputTokenCost)
	// 退役后不得再出现在公开目录或计价面。
	for _, retired := range []string{"gemini-3.6-flash", "gemini-3.5-flash-lite"} {
		require.False(t, isPublicCatalogModelSupported("gemini", retired))
		require.False(t, isPublicCatalogModelSupported(PlatformAntigravity, retired))
	}
}

func TestCandidateEligibilityGeminiNativeActivationScope(t *testing.T) {
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	wantVertex := map[string]string{
		"gemini-3-flash-preview": "gemini-3.8-flash",
		"gemini-3-flash":         "gemini-3.8-flash",
	}
	for _, model := range []string{"gemini-3-flash-preview", "gemini-3-flash"} {
		require.Equal(t, wantVertex[model], floor.NewAPIChannelTypes["41"][model])
		for profile, mapping := range floor.VertexCapabilityProfiles {
			require.Equal(t, wantVertex[model], mapping[model], "profile %s", profile)
		}
		require.Equal(t, "gemini-3.8-flash-high", floor.Platforms[PlatformAntigravity][model])
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
			if model == "gemini-3.5-flash-lite" {
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

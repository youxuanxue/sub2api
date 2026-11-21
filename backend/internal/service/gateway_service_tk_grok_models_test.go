//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
)

func TestMergeGrokNativeCatalogModels_UnionsRecommendedCuratedSet(t *testing.T) {
	modelSet := map[string]struct{}{
		"claude-sonnet-4-6": {},
	}
	mergeGrokNativeCatalogModels(PlatformGrok, modelSet)
	if _, ok := modelSet["claude-sonnet-4-6"]; !ok {
		t.Fatal("expected existing mapping keys to be preserved")
	}
	for id := range supportedGrokCatalogModels {
		_, present := modelSet[id]
		if isCatalogModelRecommended(id) {
			if !present {
				t.Fatalf("recommended grok catalog id %s must be merged", id)
			}
			continue
		}
		if present {
			t.Fatalf("withdrawn grok catalog id %s must not be advertised via GetAvailableModels union", id)
		}
	}
}

func TestMergeGrokNativeCatalogModels_NoOpForOtherPlatforms(t *testing.T) {
	modelSet := map[string]struct{}{"gpt-5": {}}
	mergeGrokNativeCatalogModels(PlatformOpenAI, modelSet)
	if _, ok := modelSet["grok-4.3"]; ok {
		t.Fatal("openai platform must not pull grok catalog models")
	}
}

func TestGetAvailableModels_GrokUnionsNativeCatalogWithChatMapping(t *testing.T) {
	resetGatewayHotpathStatsForTest()
	groupID := int64(60)
	repo := &modelsListAccountRepoStub{
		byGroup: map[int64][]Account{
			groupID: {
				{
					ID:       1,
					Platform: PlatformGrok,
					Credentials: map[string]any{
						"model_mapping": map[string]any{
							"claude-sonnet-4-6": "grok-4.3",
						},
					},
				},
			},
		},
	}
	svc := &GatewayService{
		accountRepo:        repo,
		modelsListCache:    gocache.New(time.Minute, time.Minute),
		modelsListCacheTTL: time.Minute,
	}
	models := svc.GetAvailableModels(context.Background(), &groupID, PlatformGrok)
	if len(models) == 0 {
		t.Fatal("expected non-empty grok model list from mapping keys")
	}
	foundChatAlias := false
	for _, m := range models {
		if m == "claude-sonnet-4-6" {
			foundChatAlias = true
			break
		}
	}
	if !foundChatAlias {
		t.Fatalf("grok GetAvailableModels must keep mapping keys, got %v", models)
	}
	for _, m := range models {
		if !isCatalogModelRecommended(m) && m != "claude-sonnet-4-6" {
			// Mapping may still expose a withdrawn target alias key; native
			// catalog union must not re-advertise withdrawn curated IDs.
			if _, inCatalog := supportedGrokCatalogModels[m]; inCatalog {
				t.Fatalf("GetAvailableModels must not advertise withdrawn grok catalog id %s, got %v", m, models)
			}
		}
	}
	if _, inCatalog := supportedGrokCatalogModels["grok-imagine-video"]; inCatalog && isCatalogModelRecommended("grok-imagine-video") {
		t.Fatal("grok-imagine-video must stay off the text-only grok allowlist/recommend surface")
	}
}

func TestResolve_GrokNativeCatalogWithChatOnlyMapping(t *testing.T) {
	ctx := context.Background()
	span := []Group{grp(60, PlatformGrok, 0, false)}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		60: {"claude-sonnet-4-6", "grok-4.3"},
	}))
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIChat, "grok-4.3", "")
	if err != nil || g == nil || g.Platform != PlatformGrok {
		t.Fatalf("grok-4.3 should resolve to grok group, got=%v err=%v", g, err)
	}
	if g, err = r.Resolve(ctx, universalKey(1), ShapeOpenAIVideo, "grok-imagine-video", ""); err == nil && g != nil && g.Platform == PlatformGrok {
		t.Fatalf("text-only grok surface must not resolve grok-imagine-video via curated native set, got=%v", g)
	}
}

func TestGrokNativeCatalogModelPassesAccountSchedulerWithChatOnlyMapping(t *testing.T) {
	account := &Account{
		ID:          1,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-sonnet-4-6": "grok-4.3",
			},
		},
	}

	if !account.IsModelSupported("grok-4.3") {
		t.Fatal("grok native catalog chat model must not be blocked by chat-only model_mapping")
	}
	if account.IsModelSupported("grok-imagine-video") {
		t.Fatal("text-only grok surface must not treat grok-imagine-video as a native catalog model")
	}

	scheduler := &defaultOpenAIAccountScheduler{}
	req := OpenAIAccountScheduleRequest{
		RequestedModel: "grok-4.3",
		GroupPlatform:  PlatformGrok,
	}
	if !scheduler.isAccountRequestCompatible(context.Background(), account, req) {
		t.Fatal("grok-4.3 must survive the OpenAI-compat account scheduler filter")
	}
	req.RequestedModel = "grok-imagine-video"
	if scheduler.isAccountRequestCompatible(context.Background(), account, req) {
		t.Fatal("text-only grok surface must reject grok-imagine-video in the OpenAI-compat account scheduler filter")
	}

	if account.IsModelSupported("veo-3.1-generate-001") {
		t.Fatal("non-grok video models must still be rejected by chat-only grok mappings")
	}
}

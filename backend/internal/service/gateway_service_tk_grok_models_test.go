//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
)

func TestMergeGrokNativeCatalogModels_UnionsCuratedSet(t *testing.T) {
	modelSet := map[string]struct{}{
		"claude-sonnet-4-6": {},
	}
	mergeGrokNativeCatalogModels(PlatformGrok, modelSet)
	if _, ok := modelSet["grok-imagine-video"]; !ok {
		t.Fatal("expected grok-imagine-video in merged model set")
	}
	if _, ok := modelSet["claude-sonnet-4-6"]; !ok {
		t.Fatal("expected existing mapping keys to be preserved")
	}
}

func TestMergeGrokNativeCatalogModels_NoOpForOtherPlatforms(t *testing.T) {
	modelSet := map[string]struct{}{"gpt-5": {}}
	mergeGrokNativeCatalogModels(PlatformOpenAI, modelSet)
	if _, ok := modelSet["grok-imagine-video"]; ok {
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
		t.Fatal("expected non-empty grok model list")
	}
	foundVideo := false
	for _, m := range models {
		if m == "grok-imagine-video" {
			foundVideo = true
			break
		}
	}
	if !foundVideo {
		t.Fatalf("grok GetAvailableModels must include grok-imagine-video, got %v", models)
	}
}

func TestResolve_GrokImagineVideoWithChatOnlyMapping(t *testing.T) {
	ctx := context.Background()
	span := []Group{grp(60, PlatformGrok, 0, false)}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		60: {"claude-sonnet-4-6", "grok-4.3"},
	}))
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIVideo, "grok-imagine-video", "")
	if err != nil || g == nil || g.Platform != PlatformGrok {
		t.Fatalf("grok-imagine-video should resolve to grok group, got=%v err=%v", g, err)
	}
}

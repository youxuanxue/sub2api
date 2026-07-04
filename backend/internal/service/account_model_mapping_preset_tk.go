package service

import (
	"context"
	"sort"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// AccountModelMappingPresetIDs returns TokenKey's empirically verified model IDs
// for admin account model_mapping auto-fill (Create/Edit when mapping is empty).
// Native platforms share tkServableCandidateIDs with the group selector; grok/kiro
// and newapi ch41 (Vertex SA) use their platform-specific servable sets. Other
// newapi channel types return empty — no TK-verified preset without upstream fetch.
func AccountModelMappingPresetIDs(ctx context.Context, platform string, channelType int, availability MePricingAvailability) []string {
	platform = normalizeAccountModelMappingPresetPlatform(platform)
	var ids []string
	switch platform {
	case PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok:
		ids = tkServableCandidateIDs(ctx, platform, availability)
	case PlatformKiro:
		ids = kiroModelMappingPresetIDs()
	case PlatformNewAPI:
		if channelType == newapiconstant.ChannelTypeVertexAi {
			ids = supportedCatalogModelIDsForPlatform(PlatformGemini)
		} else {
			ids = tkServedModelsManifestPresetIDsByChannelType(channelType)
		}
	default:
		return nil
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return ids
}

// NewAPIModelMappingPresetIDsForChannelType exposes preset lookup for handlers
// that cannot reach AdminService (tests, nil adminService fallback).
func NewAPIModelMappingPresetIDsForChannelType(channelType int) []string {
	return AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, channelType, nil)
}

// NewAPIModelDisplayIDsForChannelType returns the newapi model ids that may be
// shown on public catalog/menu surfaces. It deliberately excludes manifest
// display=false rows while AccountModelMappingPresetIDs keeps those rows
// available for admin provisioning/model_mapping workflows.
func NewAPIModelDisplayIDsForChannelType(channelType int) []string {
	var ids []string
	if channelType == newapiconstant.ChannelTypeVertexAi {
		ids = supportedCatalogModelIDsForPlatform(PlatformGemini)
	} else {
		ids = tkServedModelsManifestDisplayPresetIDsByChannelType(channelType)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return ids
}

// NewAPIManifestPresetChannelTypes returns channel_type values with TK-verified
// presets in tk_served_models.json (excludes Vertex ch41, which uses Gemini catalog).
func NewAPIManifestPresetChannelTypes() []int {
	loadTkServedModelsManifest()
	out := make([]int, 0, len(tkServedModelsManifestIDsByChannelType))
	for ct := range tkServedModelsManifestIDsByChannelType {
		out = append(out, ct)
	}
	sort.Ints(out)
	return out
}

func normalizeAccountModelMappingPresetPlatform(platform string) string {
	platform = strings.TrimSpace(strings.ToLower(platform))
	if platform == "claude" {
		return PlatformAnthropic
	}
	if platform == "xai" {
		return PlatformGrok
	}
	return platform
}

func kiroModelMappingPresetIDs() []string {
	models := KiroAdminTestModels()
	ids := make([]string, 0, len(models))
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

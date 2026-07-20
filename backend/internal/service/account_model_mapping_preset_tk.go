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
// and newapi ch41 (Vertex SA) use their platform-specific servable sets. Curated
// newapi channel types use the manifest projection; unknown channels return empty.
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

// NewAPIModelMappingPresetOverrideIDsForChannelType returns the TokenKey-owned
// replacement for new-api's built-in "fill related models" defaults. The bool is
// true when TokenKey intentionally owns the channel_type default, including
// retired pools whose replacement is an empty list.
func NewAPIModelMappingPresetOverrideIDsForChannelType(channelType int) ([]string, bool) {
	if channelType == newapiconstant.ChannelTypeVertexAi {
		return AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, channelType, nil), true
	}
	ids := tkServedModelsManifestPresetIDsByChannelType(channelType)
	if len(ids) > 0 {
		sort.Strings(ids)
		return ids, true
	}
	if isNewAPIEmptyModelMappingPresetOverrideChannelType(channelType) {
		return nil, true
	}
	return nil, false
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

// NewAPIModelMappingPresetOverrideChannelTypes returns the channel_type values
// where TokenKey must replace new-api's static model default list.
func NewAPIModelMappingPresetOverrideChannelTypes() []int {
	loadTkServedModelsManifest()
	seen := map[int]struct{}{
		newapiconstant.ChannelTypeVertexAi: {},
	}
	for ct := range tkServedModelsManifestIDsByChannelType {
		seen[ct] = struct{}{}
	}
	for ct := range newAPIEmptyModelMappingPresetOverrideChannelTypes {
		seen[ct] = struct{}{}
	}
	out := make([]int, 0, len(seen))
	for ct := range seen {
		out = append(out, ct)
	}
	sort.Ints(out)
	return out
}

var newAPIEmptyModelMappingPresetOverrideChannelTypes = map[int]struct{}{
	// GLM direct account/group was retired. GLM display/serving intent now rides
	// Qwen/China newapi pools, so ch26 must not fall back to new-api's stale
	// built-in GLM defaults. If a direct GLM pool is provisioned again, adding
	// manifest rows for ch26 will override this empty replacement.
	newapiconstant.ChannelTypeZhipu_v4: {},
}

func isNewAPIEmptyModelMappingPresetOverrideChannelType(channelType int) bool {
	_, ok := newAPIEmptyModelMappingPresetOverrideChannelTypes[channelType]
	return ok
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

package service

import (
	"context"
	"sort"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
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
			ids = vertexSharedModelMappingPresetIDs()
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

func isNewAPIVolcEngineAgentPlanAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformNewAPI &&
		account.ChannelType == newapiconstant.ChannelTypeVolcEngine &&
		newapiintegration.IsVolcEngineAgentPlanBaseURL(account.ChannelType, account.GetBaseURL())
}

func isNewAPIQianfanTokenPlanAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformNewAPI &&
		account.ChannelType == newapiconstant.ChannelTypeBaiduV2 &&
		newapiintegration.IsQianfanTokenPlanBaseURL(account.ChannelType, account.GetBaseURL())
}

func isNewAPIAliTokenPlanAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformNewAPI &&
		account.ChannelType == newapiconstant.ChannelTypeAli &&
		newapiintegration.IsAliTokenPlanBaseURL(account.ChannelType, account.GetBaseURL())
}

func isNewAPIQianfanAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformNewAPI &&
		account.ChannelType == newapiconstant.ChannelTypeBaiduV2 &&
		newapiintegration.IsQianfanBaseURL(account.ChannelType, account.GetBaseURL())
}

// isNewAPIXRTokenAccount reports whether this is an XRToken account — an
// ARK-compatible reseller provisioned on the video-only DoubaoVideo channel.
// Its preset surface comes from the manifest's XRToken property scope, which
// includes both primary ch54 rows and shared rows indexed under ch45.
func isNewAPIXRTokenAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformNewAPI &&
		account.ChannelType == newapiconstant.ChannelTypeDoubaoVideo &&
		newapiintegration.IsXRTokenBaseURL(account.ChannelType, account.GetBaseURL())
}

// accountModelMappingOverrideAccounts declares account-specific mapping scopes
// whose serving intent is narrower than their shared platform/channel floor.
func accountModelMappingOverrideAccounts() []*Account {
	return []*Account{
		{
			Platform:    PlatformNewAPI,
			Type:        AccountTypeAPIKey,
			ChannelType: newapiconstant.ChannelTypeVolcEngine,
			Credentials: map[string]any{
				"base_url": newapiintegration.VolcEngineAgentPlanBaseURL,
			},
		},
		{
			Platform:    PlatformNewAPI,
			Type:        AccountTypeAPIKey,
			ChannelType: newapiconstant.ChannelTypeBaiduV2,
			Credentials: map[string]any{
				"base_url": newapiintegration.QianfanBaseURL,
			},
		},
		// Ali MaaS Token Plan shares ChannelTypeAli with PAYG DashScope and is
		// told apart only by base_url. Without this override, check-accounts
		// compares the account against the generic ch17 floor and reports false
		// "missing SSOT keys" for every PAYG-only curated id.
		{
			Platform:    PlatformNewAPI,
			Type:        AccountTypeAPIKey,
			ChannelType: newapiconstant.ChannelTypeAli,
			Credentials: map[string]any{
				"base_url": newapiintegration.AliTokenPlanBaseURL,
			},
		},
		// Qianfan Token Plan Person shares ChannelTypeBaiduV2 with PAYG Qianfan.
		{
			Platform:    PlatformNewAPI,
			Type:        AccountTypeAPIKey,
			ChannelType: newapiconstant.ChannelTypeBaiduV2,
			Credentials: map[string]any{
				"base_url": newapiintegration.QianfanTokenPlanBaseURL,
			},
		},
		// XRToken shares ChannelTypeDoubaoVideo with official Ark and is told
		// apart only by base_url, so its manifest property scope is narrower
		// than the generic ch54 floor. Without this entry the bundle carries no
		// XRToken account override, and `modelops activate` fails closed because
		// the account it is trying to activate is invisible to the floor.
		{
			Platform:    PlatformNewAPI,
			Type:        AccountTypeAPIKey,
			ChannelType: newapiconstant.ChannelTypeDoubaoVideo,
			Credentials: map[string]any{
				"base_url": newapiintegration.XRTokenBaseURL,
			},
		},
	}
}

// NewAPIModelMappingPresetIDsForAccount preserves account-specific serving
// intent for channel types shared by multiple VolcEngine products.
func NewAPIModelMappingPresetIDsForAccount(account *Account) []string {
	if account == nil {
		return nil
	}
	if account.ChannelType == newapiconstant.ChannelTypeVertexAi {
		ids, _ := vertexCapabilityProfileModelMappingIDs(account.VertexCapabilityProfile())
		return ids
	}
	if isNewAPIVolcEngineAgentPlanAccount(account) {
		ids := tkServedModelsManifestPresetIDsForSelector(account.Platform, account.ChannelType, account.GetBaseURL())
		sort.Strings(ids)
		return ids
	}
	if isNewAPIAliTokenPlanAccount(account) {
		ids := newAPIAliTokenPlanModelMappingPresetIDs()
		sort.Strings(ids)
		return ids
	}
	if isNewAPIQianfanTokenPlanAccount(account) {
		ids := newAPIQianfanTokenPlanModelMappingPresetIDs()
		sort.Strings(ids)
		return ids
	}
	if isNewAPIQianfanAccount(account) {
		ids := newAPIQianfanModelMappingPresetIDs()
		sort.Strings(ids)
		return ids
	}
	if isNewAPIXRTokenAccount(account) {
		ids := newAPIXRTokenModelMappingPresetIDs()
		sort.Strings(ids)
		return ids
	}
	return AccountModelMappingPresetIDs(context.Background(), account.Platform, account.ChannelType, nil)
}

// NewAPIModelDisplayIDsForAccount is the user-facing projection of the same
// account-specific manifest intent. Hidden rows remain available to admin
// provisioning through NewAPIModelMappingPresetIDsForAccount.
func NewAPIModelDisplayIDsForAccount(account *Account) []string {
	if account == nil {
		return nil
	}
	if isNewAPIVolcEngineAgentPlanAccount(account) {
		ids := tkServedModelsManifestDisplayPresetIDsForSelector(account.Platform, account.ChannelType, account.GetBaseURL())
		sort.Strings(ids)
		return ids
	}
	if isNewAPIAliTokenPlanAccount(account) {
		ids := newAPIAliTokenPlanModelDisplayPresetIDs()
		sort.Strings(ids)
		return ids
	}
	if isNewAPIQianfanTokenPlanAccount(account) {
		ids := newAPIQianfanTokenPlanModelDisplayPresetIDs()
		sort.Strings(ids)
		return ids
	}
	if isNewAPIQianfanAccount(account) {
		ids := newAPIQianfanModelDisplayPresetIDs()
		sort.Strings(ids)
		return ids
	}
	if isNewAPIXRTokenAccount(account) {
		ids := newAPIXRTokenModelDisplayPresetIDs()
		sort.Strings(ids)
		return ids
	}
	return NewAPIModelDisplayIDsForChannelType(account.ChannelType)
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
	ids := make([]string, 0, len(supportedKiroCatalogModels))
	for id := range supportedKiroCatalogModels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

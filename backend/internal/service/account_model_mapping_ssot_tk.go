package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

const (
	accountModelMappingPlatformBedrock                = "bedrock"
	accountModelMappingPlatformOpenAIAinzyRelay       = "openai_ainzy_relay"
	accountModelMappingPlatformOpenAITokenseaRelay    = "openai_tokensea_relay"
	accountModelMappingPlatformAnthropicTokenseaRelay = "anthropic_tokensea_relay"
	accountModelMappingPlatformOpenAICloudwiseRelay   = "openai_cloudwise_relay"
)

// accountModelMappingRuntime is the hot runtime replacement layer for the
// compiled model_mapping floor. If a platform/channel appears here, it replaces
// the compiled floor for that scope; absent scopes keep the compiled floor.
type accountModelMappingRuntime struct {
	platforms          map[string]map[string]string
	newAPIChannelTypes map[int]map[string]string
}

type accountModelMappingRuntimeDoc struct {
	Platforms          map[string]map[string]string `json:"platforms"`
	NewAPIChannelTypes map[string]map[string]string `json:"newapi_channel_types"`
}

// AccountModelMappingFloorDoc is the ops-facing export of the effective
// account model_mapping floor. Platform/newapi scopes are full replacements.
type AccountModelMappingFloorDoc struct {
	Platforms                     map[string]map[string]string  `json:"platforms"`
	NewAPIChannelTypes            map[string]map[string]string  `json:"newapi_channel_types"`
	AccountOverrides              []AccountModelMappingOverride `json:"account_overrides"`
	VertexCapabilityProfiles      map[string]map[string]string  `json:"vertex_capability_profiles"`
	AntigravityScopes             []string                      `json:"antigravity_group_scopes"`
	ForbiddenModelMappingKeys     map[string][]string           `json:"forbidden_model_mapping_keys,omitempty"`
	ForbiddenModelMappingPrefixes map[string][]string           `json:"forbidden_model_mapping_prefixes,omitempty"`
}

// AccountModelMappingOverride is a full replacement for accounts matching the
// platform/channel/base_url selector. It deliberately has no account-id key so
// account identity remains property-based when an ID is reused.
type AccountModelMappingOverride struct {
	Platform     string            `json:"platform"`
	ChannelType  int               `json:"channel_type,omitempty"`
	BaseURL      string            `json:"base_url"`
	ModelMapping map[string]string `json:"model_mapping"`
}

const ModelSurfaceBundleSchemaVersion = 4

// ModelSurfaceBundle is the deterministic release artifact consumed by modelops.
// The digest covers the Go-owned floor projection, not mutable release metadata.
type ModelSurfaceBundle struct {
	SchemaVersion       int                          `json:"schema_version"`
	FloorSHA256         string                       `json:"floor_sha256"`
	AccountModelMapping *AccountModelMappingFloorDoc `json:"account_model_mapping"`
}

func parseAccountModelMappingRuntime(raw string) (*accountModelMappingRuntime, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var doc accountModelMappingRuntimeDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, err
	}
	rt := &accountModelMappingRuntime{
		platforms:          make(map[string]map[string]string, len(doc.Platforms)),
		newAPIChannelTypes: make(map[int]map[string]string, len(doc.NewAPIChannelTypes)),
	}
	for platform, mapping := range doc.Platforms {
		p := normalizeAccountModelMappingPresetPlatform(platform)
		if p == "" {
			return nil, fmt.Errorf("empty platform key")
		}
		cleaned, err := normalizeRuntimeModelMapping(mapping)
		if err != nil {
			return nil, fmt.Errorf("platform %s: %w", p, err)
		}
		rt.platforms[p] = cleaned
	}
	for rawCT, mapping := range doc.NewAPIChannelTypes {
		ct, err := strconv.Atoi(strings.TrimSpace(rawCT))
		if err != nil || ct <= 0 {
			return nil, fmt.Errorf("invalid newapi channel_type %q", rawCT)
		}
		cleaned, err := normalizeRuntimeModelMapping(mapping)
		if err != nil {
			return nil, fmt.Errorf("newapi channel_type %d: %w", ct, err)
		}
		rt.newAPIChannelTypes[ct] = cleaned
	}
	return rt, nil
}

func normalizeRuntimeModelMapping(mapping map[string]string) (map[string]string, error) {
	if len(mapping) == 0 {
		return nil, fmt.Errorf("model_mapping must be non-empty")
	}
	out := make(map[string]string, len(mapping))
	for k, v := range mapping {
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			return nil, fmt.Errorf("model_mapping contains empty key/value")
		}
		out[key] = val
	}
	return out, nil
}

func accountModelMappingForAccount(ctx context.Context, account *Account, pricing *PricingCatalogService, availability MePricingAvailability, runtime *accountModelMappingRuntime) (map[string]string, bool) {
	if account == nil {
		return nil, false
	}
	scope := accountModelMappingScopeForAccount(account)
	if scope == "" {
		return nil, false
	}
	if scope == PlatformNewAPI {
		if account.ChannelType == newapiconstant.ChannelTypeVertexAi {
			mapping, _ := vertexModelMappingForAccount(account)
			return mapping, len(mapping) > 0
		}
		if isNewAPIVolcEngineAgentPlanAccount(account) {
			ids := NewAPIModelMappingPresetIDsForAccount(account)
			if len(ids) == 0 {
				return nil, false
			}
			return identityModelMapping(ids), true
		}
		if isNewAPIQianfanAccount(account) {
			ids := NewAPIModelMappingPresetIDsForAccount(account)
			if len(ids) == 0 {
				return nil, false
			}
			return identityModelMapping(ids), true
		}
		// XRToken (ch54 + sentinel base_url) serves the manifest's five-model
		// property scope. The generic channel-type fallback must not own this
		// account: XRToken-only rows are base_url-scoped, and three shared rows
		// are indexed under channel_type 45. Route through the scoped account
		// preset so apply-accounts preserves exactly the declared five models.
		//
		// The mapping stays IDENTITY: XRToken's `volcengine/` vendor namespace is
		// applied on the wire by the task adaptor
		// (newapiintegration.XRTokenUpstreamVideoModel), not stored as a mapping
		// target the floor cannot represent and apply-accounts would revert.
		if isNewAPIXRTokenAccount(account) {
			ids := NewAPIModelMappingPresetIDsForAccount(account)
			if len(ids) == 0 {
				return nil, false
			}
			return identityModelMapping(ids), true
		}
		if runtime != nil {
			if mapping, ok := runtime.newAPIChannelTypes[account.ChannelType]; ok {
				return cloneStringMap(mapping), true
			}
		}
		ids := NewAPIModelDisplayIDsForChannelType(account.ChannelType)
		if len(ids) == 0 {
			return nil, false
		}
		return identityModelMapping(ids), true
	}
	if runtime != nil {
		if mapping, ok := runtime.platforms[scope]; ok {
			return cloneStringMap(mapping), true
		}
	}
	switch scope {
	case PlatformOpenAI:
		if account.IsOpenAIAinzyRelay() {
			return openAIAinzyRelayAccountModelMappingFloor(ctx, pricing, availability), true
		}
		if account.IsOpenAITokenseaRelay() {
			return openAITokenseaRelayAccountModelMappingFloor(ctx, pricing, availability), true
		}
		if account.IsOpenAICloudwiseRelay() {
			return openAICloudwiseRelayAccountModelMappingFloor(ctx, pricing, availability), true
		}
		return openAICanonicalAccountModelMappingFloor(ctx, pricing, availability), true
	case PlatformAnthropic, PlatformGemini:
		if scope == PlatformAnthropic && account.IsAnthropicTokenseaRelay() {
			return anthropicTokenseaRelayModelMappingFloor(), true
		}
		ids := ServableClientFacingIDs(ctx, scope, availability, pricing)
		if len(ids) == 0 {
			ids = supportedCatalogModelIDsForPlatform(scope)
		}
		if len(ids) == 0 {
			return nil, false
		}
		return identityModelMapping(ids), true
	case PlatformAntigravity:
		return antigravityAccountModelMappingFloor(ctx, pricing, availability), true
	case PlatformGrok:
		return grokAccountModelMappingFloor(ctx, pricing, availability), true
	case PlatformKiro:
		return identityModelMapping(kiroModelMappingPresetIDs()), true
	case accountModelMappingPlatformBedrock:
		return cloneStringMap(domain.DefaultBedrockModelMapping), true
	default:
		return nil, false
	}
}

// AccountModelMappingFloorForOps returns the compiled floor plus an optional
// runtime replacement layer. It is intentionally used by ops tooling instead of
// duplicating the SSOT in Python.
func AccountModelMappingFloorForOps(ctx context.Context, runtimeRaw string) (*AccountModelMappingFloorDoc, error) {
	runtime, err := parseAccountModelMappingRuntime(runtimeRaw)
	if err != nil {
		return nil, err
	}
	out := &AccountModelMappingFloorDoc{
		Platforms:                     make(map[string]map[string]string),
		NewAPIChannelTypes:            make(map[string]map[string]string),
		AccountOverrides:              make([]AccountModelMappingOverride, 0),
		VertexCapabilityProfiles:      vertexCapabilityProfileMappingsForOps(),
		AntigravityScopes:             append([]string(nil), canonicalAntigravityModelScopes...),
		ForbiddenModelMappingKeys:     accountModelMappingForbiddenKeysByScope(),
		ForbiddenModelMappingPrefixes: accountModelMappingForbiddenPrefixesByScope(),
	}
	for _, keys := range out.ForbiddenModelMappingKeys {
		sort.Strings(keys)
	}
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok, PlatformKiro} {
		mapping, ok := accountModelMappingForAccount(ctx, &Account{Platform: platform}, nil, nil, runtime)
		if ok && len(mapping) > 0 {
			out.Platforms[platform] = cloneStringMap(mapping)
		}
	}
	relayMapping, ok := accountModelMappingForAccount(ctx, &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.ainzy.net/v1",
		},
	}, nil, nil, runtime)
	if ok && len(relayMapping) > 0 {
		out.Platforms[accountModelMappingPlatformOpenAIAinzyRelay] = cloneStringMap(relayMapping)
	}
	tokenseaMapping, ok := accountModelMappingForAccount(ctx, &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://agent.tokensea.ai",
		},
	}, nil, nil, runtime)
	if ok && len(tokenseaMapping) > 0 {
		out.Platforms[accountModelMappingPlatformOpenAITokenseaRelay] = cloneStringMap(tokenseaMapping)
	}
	cloudwiseMapping, ok := accountModelMappingForAccount(ctx, &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.cloudwise.ai/api",
		},
	}, nil, nil, runtime)
	if ok && len(cloudwiseMapping) > 0 {
		out.Platforms[accountModelMappingPlatformOpenAICloudwiseRelay] = cloneStringMap(cloudwiseMapping)
	}
	anthropicTokenseaMapping, ok := accountModelMappingForAccount(ctx, &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://agent.tokensea.ai",
		},
	}, nil, nil, runtime)
	if ok && len(anthropicTokenseaMapping) > 0 {
		out.Platforms[accountModelMappingPlatformAnthropicTokenseaRelay] = cloneStringMap(anthropicTokenseaMapping)
	}
	bedrock, ok := accountModelMappingForAccount(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}, nil, nil, runtime)
	if ok && len(bedrock) > 0 {
		out.Platforms[accountModelMappingPlatformBedrock] = cloneStringMap(bedrock)
	}

	channelTypes := map[int]struct{}{
		newapiconstant.ChannelTypeVertexAi: {},
	}
	for _, ct := range NewAPIManifestPresetChannelTypes() {
		channelTypes[ct] = struct{}{}
	}
	if runtime != nil {
		for ct := range runtime.newAPIChannelTypes {
			channelTypes[ct] = struct{}{}
		}
	}
	sortedCT := make([]int, 0, len(channelTypes))
	for ct := range channelTypes {
		sortedCT = append(sortedCT, ct)
	}
	sort.Ints(sortedCT)
	for _, ct := range sortedCT {
		mapping, ok := accountModelMappingForAccount(ctx, &Account{Platform: PlatformNewAPI, ChannelType: ct}, nil, nil, runtime)
		if ok && len(mapping) > 0 {
			out.NewAPIChannelTypes[strconv.Itoa(ct)] = cloneStringMap(mapping)
		}
	}
	for _, account := range accountModelMappingOverrideAccounts() {
		mapping, ok := accountModelMappingForAccount(ctx, account, nil, nil, runtime)
		baseURL := normalizeAccountModelMappingOverrideBaseURL(account.GetBaseURL())
		if !ok || len(mapping) == 0 || baseURL == "" {
			continue
		}
		out.AccountOverrides = append(out.AccountOverrides, AccountModelMappingOverride{
			Platform:     account.Platform,
			ChannelType:  account.ChannelType,
			BaseURL:      baseURL,
			ModelMapping: cloneStringMap(mapping),
		})
	}
	sort.Slice(out.AccountOverrides, func(i, j int) bool {
		left, right := out.AccountOverrides[i], out.AccountOverrides[j]
		if left.Platform != right.Platform {
			return left.Platform < right.Platform
		}
		if left.ChannelType != right.ChannelType {
			return left.ChannelType < right.ChannelType
		}
		return left.BaseURL < right.BaseURL
	})
	return out, nil
}

func normalizeAccountModelMappingOverrideBaseURL(raw string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(raw)), "/")
}

func accountModelMappingForbiddenKeysByScope() map[string][]string {
	return map[string][]string{
		// Kiro-backed Claude models remain public under the anthropic vendor,
		// but native Anthropic accounts must not inherit Kiro-only capability.
		// Kiro mirror stubs resolve to PlatformKiro before this policy applies.
		PlatformAnthropic: kiroExclusiveModelIDs(),
		PlatformAntigravity: append(
			domain.AntigravityStructuralDeadModelMappingKeys(),
			domain.AntigravityUnpricedModelMappingKeys()...,
		),
		accountModelMappingPlatformOpenAITokenseaRelay: tokenseaRelayForbiddenUpstreamIDs(
			supportedOpenAITokenseaRelayCatalogModels,
		),
		accountModelMappingPlatformAnthropicTokenseaRelay: tokenseaRelayForbiddenUpstreamIDs(
			supportedAnthropicTokenseaRelayCatalogModels,
		),
	}
}

func accountModelMappingForbiddenPrefixesByScope() map[string][]string {
	return map[string][]string{
		PlatformAntigravity: {"gpt-oss-"},
	}
}

func kiroExclusiveModelIDs() []string {
	out := make([]string, 0)
	for id := range supportedKiroCatalogModels {
		if _, native := supportedAnthropicCatalogModels[id]; !native {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// ModelSurfaceBundleForOps exports one checksummed artifact from the Go owner.
// Runtime JSON is accepted for focused tests; release bundles use the compiled
// floor with an empty runtime replacement.
func ModelSurfaceBundleForOps(ctx context.Context, runtimeRaw string) (*ModelSurfaceBundle, error) {
	floor, err := AccountModelMappingFloorForOps(ctx, runtimeRaw)
	if err != nil {
		return nil, err
	}
	payload, err := canonicalModelSurfaceFloorJSON(floor)
	if err != nil {
		return nil, fmt.Errorf("marshal account model_mapping floor: %w", err)
	}
	digest := sha256.Sum256(payload)
	return &ModelSurfaceBundle{
		SchemaVersion:       ModelSurfaceBundleSchemaVersion,
		FloorSHA256:         fmt.Sprintf("%x", digest),
		AccountModelMapping: floor,
	}, nil
}

// canonicalModelSurfaceFloorJSON removes Go struct field order from the digest
// contract so non-Go rollout consumers can verify it with canonical JSON.
func canonicalModelSurfaceFloorJSON(floor *AccountModelMappingFloorDoc) ([]byte, error) {
	raw, err := json.Marshal(floor)
	if err != nil {
		return nil, err
	}
	var projection any
	if err := json.Unmarshal(raw, &projection); err != nil {
		return nil, err
	}
	return json.Marshal(projection)
}

func accountModelMappingScopeForAccount(account *Account) string {
	if account == nil {
		return ""
	}
	switch {
	case account.IsKiroMirrorStub() || account.IsKiro():
		return PlatformKiro
	case account.IsBedrock():
		return accountModelMappingPlatformBedrock
	default:
		return normalizeAccountModelMappingPresetPlatform(account.Platform)
	}
}

func openAICanonicalAccountModelMappingFloor(ctx context.Context, pricing *PricingCatalogService, availability MePricingAvailability) map[string]string {
	ids := ServableClientFacingIDs(ctx, PlatformOpenAI, availability, pricing)
	if len(ids) == 0 {
		ids = supportedCatalogModelIDsForPlatform(PlatformOpenAI)
	}
	if len(ids) == 0 {
		return nil
	}
	return identityModelMapping(ids)
}

func openAIAinzyRelayAccountModelMappingFloor(ctx context.Context, pricing *PricingCatalogService, availability MePricingAvailability) map[string]string {
	ids := supportedCatalogModelIDsFromMap(supportedOpenAIAinzyRelayCatalogModels)
	if len(ids) == 0 {
		return nil
	}
	return identityModelMapping(ids)
}

func openAITokenseaRelayAccountModelMappingFloor(ctx context.Context, pricing *PricingCatalogService, availability MePricingAvailability) map[string]string {
	_ = ctx
	_ = pricing
	_ = availability
	ids := tokenseaRelayCorePublicFloorIDs()
	if len(ids) == 0 {
		return nil
	}
	return identityModelMapping(ids)
}

func openAICloudwiseRelayAccountModelMappingFloor(ctx context.Context, pricing *PricingCatalogService, availability MePricingAvailability) map[string]string {
	_ = ctx
	_ = pricing
	_ = availability
	return cloneStringMap(openAICloudwiseRelayWildcardModelMappingFloor())
}

func anthropicTokenseaRelayModelMappingFloor() map[string]string {
	ids := tokenseaRelayPublicSSOTIDs(append(
		append([]string{}, tokenseaRelayCorePublicFloorIDs()...),
		supportedCatalogModelIDsFromMap(supportedAnthropicTokenseaRelayCatalogModels)...,
	))
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if wire, ok := anthropicTokenseaRelayWireModelMapping[id]; ok {
			out[id] = wire
			continue
		}
		out[id] = id
	}
	return out
}

// tokenseaRelaySharedExtraSSOTIDs are public CatalogPolicy models already
// served on prod account 92 but absent from the 47-id upstream snapshot.
var tokenseaRelaySharedExtraSSOTIDs = []string{
	"codex-auto-review",
	"gpt-5.3-codex-spark",
	"gpt-5.6",
	"gpt-5.6-luna",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
}

func tokenseaRelaySupportsRequestedModel(requestedModel string) bool {
	normalized := strings.TrimSpace(requestedModel)
	if normalized == "" {
		return false
	}
	for _, id := range tokenseaRelayCorePublicFloorIDs() {
		if strings.EqualFold(id, normalized) {
			return true
		}
	}
	return false
}

func tokenseaRelayAccountSupportsRequestedModel(requestedModel string) bool {
	return tokenseaRelaySupportsRequestedModel(requestedModel) ||
		tkIsForwardableAnthropicModelName(requestedModel)
}

// tokenseaRelayCorePublicFloorIDs is the shared 92/93 live floor: upstream 47
// intersect the public catalog projection, plus the extras 92 already serves.
func tokenseaRelayCorePublicFloorIDs() []string {
	return tokenseaRelayPublicSSOTIDs(append(
		supportedCatalogModelIDsFromMap(supportedOpenAITokenseaRelayCatalogModels),
		tokenseaRelaySharedExtraSSOTIDs...,
	))
}

// tokenseaRelayPublicSSOTIDs keeps only upstream-listed IDs that satisfy the
// public catalog policy: displayable, priced, and backed by reviewed path evidence.
func tokenseaRelayPublicSSOTIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !tokenseaRelayMeetsPublicSSOT(id) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func tokenseaRelayMeetsPublicSSOT(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if tokenseaRelayIsClientFacing(id) && tokenseaRelayIsPriced(id) {
		return true
	}
	// Official dated wire IDs stay first-class when they alias a client-facing catalog row.
	for short, wire := range anthropicTokenseaRelayWireModelMapping {
		if wire == id && tokenseaRelayIsClientFacing(short) && tokenseaRelayIsPriced(short) {
			return true
		}
	}
	return false
}

func tokenseaRelayIsClientFacing(id string) bool {
	if _, ok := supportedOpenAICatalogModels[id]; ok {
		return true
	}
	if _, ok := supportedClaudeCatalogModels[id]; ok {
		return true
	}
	if _, ok := supportedGeminiCatalogModels[id]; ok {
		return true
	}
	if _, ok := supportedAntigravityCatalogModels[id]; ok {
		return true
	}
	// Listing on tokensea GET /v1/models is not serving. Prod account 92
	// raw POST /v1/chat/completions on 2026-08-20: gpt-5.4/claude-fable-5
	// returned 200; deepseek-v3.2, qwen3.7-max, glm-5, glm-5.2, kimi-k2.5,
	// kimi-k3, deepseek-v4-flash, deepseek-v4-pro returned 400 openai_error
	// (same body as user16); minimax-m2.7 returned 410 EOL;
	// deepseek-v4-flash-0731 returned 503 model_not_found. Keep them off
	// the GPT 专线 floor so universal keys stay on vendor newapi accounts.
	return false
}

func tokenseaRelayIsPriced(id string) bool {
	if snap := loadTKPricingOverlaySnapshot(); snap != nil {
		if pricing := snap.Models[id]; pricing != nil {
			return true
		}
	}
	return isTkCuratedNewAPIModelListed(id)
}

func tokenseaRelayForbiddenUpstreamIDs(upstream map[string]struct{}) []string {
	out := make([]string, 0)
	for id := range upstream {
		if !tokenseaRelayMeetsPublicSSOT(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

var anthropicTokenseaRelayWireModelMapping = map[string]string{
	"claude-fable-5":            "claude-fable-5",
	"claude-haiku-4-5":          "claude-haiku-4-5-20251001",
	"claude-haiku-4-5-20251001": "claude-haiku-4-5-20251001",
	"claude-opus-4-5":           "claude-opus-4-5-20251101",
	"claude-opus-4-5-20251101":  "claude-opus-4-5-20251101",
	"claude-opus-4-6":           "claude-opus-4-6",
	"claude-opus-4-7":           "claude-opus-4-7",
	"claude-opus-4-8":           "claude-opus-4-8",
	"claude-opus-5":             "claude-opus-5",
	"claude-sonnet-4-6":         "claude-sonnet-4-6",
	"claude-sonnet-5":           "claude-sonnet-5",
}

func supportedCatalogModelIDsFromMap(src map[string]struct{}) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, 0, len(src))
	for id := range src {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func antigravityAccountModelMappingFloor(ctx context.Context, pricing *PricingCatalogService, availability MePricingAvailability) map[string]string {
	displayIDs := ServableClientFacingIDs(ctx, PlatformAntigravity, availability, pricing)
	if len(displayIDs) == 0 {
		displayIDs = supportedCatalogModelIDsForPlatform(PlatformAntigravity)
	}
	displaySet := stringSet(displayIDs)
	out := make(map[string]string)
	for from, to := range domain.DefaultAntigravityModelMapping {
		if strings.HasPrefix(from, "gpt-oss-") {
			continue
		}
		if domain.IsAntigravityStructuralDeadModelMappingKey(from) || domain.IsAntigravityUnpricedModelMappingKey(from) {
			continue
		}
		if _, ok := displaySet[from]; ok {
			out[from] = to
			continue
		}
		if _, ok := displaySet[to]; ok {
			out[from] = to
		}
	}
	return out
}

func grokAccountModelMappingFloor(ctx context.Context, pricing *PricingCatalogService, availability MePricingAvailability) map[string]string {
	displayIDs := ServableClientFacingIDs(ctx, PlatformGrok, availability, pricing)
	if len(displayIDs) == 0 {
		displayIDs = supportedCatalogModelIDsForPlatform(PlatformGrok)
	}
	displaySet := stringSet(displayIDs)
	out := identityModelMapping(displayIDs)
	// Compatibility aliases win over identity even when the alias id is also
	// servable/display-listed (e.g. grok-4.5-latest → grok-4.5). Skipping
	// public-listed keys left identity remaps that violate GROK_REQUIRED_ALIASES.
	for from, to := range xai.DefaultModelMapping() {
		if _, ok := displaySet[to]; ok {
			out[from] = to
		}
	}
	for from, to := range tkGrokCompatibilityAliases {
		if _, ok := displaySet[to]; ok {
			out[from] = to
		}
	}
	return out
}

var tkGrokCompatibilityAliases = map[string]string{
	"grok-4-fast-reasoning": "grok-4.3",
	"grok-4.3-latest":       "grok-4.3",
	"grok-4.5-latest":       "grok-4.5",
	"grok-build-latest":     "grok-4.5",
	"grok-code-fast":        "grok-build-0.1",
	"grok-code-fast-1-0825": "grok-build-0.1",
}

func identityModelMapping(ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = id
		}
	}
	return out
}

func stringSet(ids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func modelMappingToAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func accountRawModelMapping(account *Account) map[string]string {
	if account == nil || account.Credentials == nil {
		return nil
	}
	return stringMappingFromRaw(account.Credentials["model_mapping"])
}

func reconciledAccountModelMapping(account *Account, required map[string]string) map[string]string {
	current := accountRawModelMapping(account)
	scope := accountModelMappingScopeForAccount(account)
	forbiddenKeys := stringSet(accountModelMappingForbiddenKeysByScope()[scope])
	forbiddenPrefixes := accountModelMappingForbiddenPrefixesByScope()[scope]
	out := make(map[string]string, len(current)+len(required))
	for key, target := range current {
		if _, forbidden := forbiddenKeys[key]; forbidden {
			continue
		}
		blocked := false
		for _, prefix := range forbiddenPrefixes {
			if strings.HasPrefix(key, prefix) {
				blocked = true
				break
			}
		}
		if !blocked {
			out[key] = target
		}
	}
	for key, target := range required {
		out[key] = target
	}
	return out
}

func modelMappingsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if b[k] != av {
			return false
		}
	}
	return true
}

func modelMappingSignatureString(mapping map[string]string) string {
	if len(mapping) == 0 {
		return ""
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		_, _ = b.WriteString(k)
		_ = b.WriteByte('=')
		_, _ = b.WriteString(mapping[k])
		_ = b.WriteByte('\n')
	}
	return b.String()
}

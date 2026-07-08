package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

const accountModelMappingPlatformBedrock = "bedrock"

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
	case PlatformAnthropic, PlatformOpenAI, PlatformGemini:
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
	"grok-4.3-latest":       "grok-4.3",
	"grok-4-fast-reasoning": "grok-4.3",
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

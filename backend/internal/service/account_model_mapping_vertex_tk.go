package service

import (
	"sort"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// VertexCapabilityProfileCredentialKey is the only account capability profile
// selector in TokenKey. It is intentionally limited to newapi Vertex ch41,
// whose service-account projects expose different model sets.
const VertexCapabilityProfileCredentialKey = "vertex_capability_profile"

const (
	vertexCapabilityProfileCorePro                   = "core-pro"
	vertexCapabilityProfileCoreProImagenStandard     = "core-pro-imagen-standard"
	vertexCapabilityProfileCoreProImagenFastStandard = "core-pro-imagen-fast-standard"
	vertexCapabilityProfileCoreImagenUltra           = "core-imagen-ultra"
)

// vertexSharedModelMappingIDs is the converged Vertex ch41 public request surface.
// Profile extras stay empty until a named capability is re-verified.
var vertexSharedModelMappingIDs = []string{
	"gemini-3-flash",
	"gemini-3-flash-preview",
	"gemini-3.5-flash-lite",
	"gemini-3.6-flash",
	"gemini-3.7-flash",
	"gemini-3.8-flash",
	"gemini-embedding-001",
	"veo-3.1-generate-001",
}

var vertexSharedModelMapping = map[string]string{
	"gemini-3.6-flash":       "gemini-3.6-flash",
	"gemini-3.7-flash":       "gemini-3.7-flash",
	"gemini-3.8-flash":       "gemini-3.8-flash",
	"gemini-3-flash":         "gemini-3.8-flash",
	"gemini-3-flash-preview": "gemini-3.8-flash",
	"gemini-3.5-flash-lite":  "gemini-3.6-flash",
	"gemini-embedding-001":   "gemini-embedding-001",
	"veo-3.1-generate-001":   "veo-3.1-generate-001",
}

var vertexCapabilityProfileExtraIDs = map[string][]string{
	vertexCapabilityProfileCorePro:                   {},
	vertexCapabilityProfileCoreProImagenStandard:     {},
	vertexCapabilityProfileCoreProImagenFastStandard: {},
	vertexCapabilityProfileCoreImagenUltra:           {},
}

// VertexCapabilityProfile returns the normalized ch41 capability selector.
// It never interprets account identity or name as capability evidence.
func (a *Account) VertexCapabilityProfile() string {
	if a == nil || a.Platform != PlatformNewAPI || a.ChannelType != newapiconstant.ChannelTypeVertexAi {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(a.GetCredential(VertexCapabilityProfileCredentialKey)))
}

func copyVertexModelMapping(src map[string]string) map[string]string {
	out := make(map[string]string, len(src))
	for from, to := range src {
		out[from] = to
	}
	return out
}

func vertexSharedModelMappingPreset() map[string]string {
	return copyVertexModelMapping(vertexSharedModelMapping)
}

func vertexSharedModelMappingPresetIDs() []string {
	return append([]string(nil), vertexSharedModelMappingIDs...)
}

func vertexSharedModelMappingKeysMatchIDs() bool {
	if len(vertexSharedModelMappingIDs) != len(vertexSharedModelMapping) {
		return false
	}
	for _, id := range vertexSharedModelMappingIDs {
		if _, ok := vertexSharedModelMapping[id]; !ok {
			return false
		}
	}
	return true
}

// Public Vertex discovery is the union of the verified capability floors.
func vertexModelDisplayIDs() []string {
	ids := stringSet(vertexSharedModelMappingPresetIDs())
	for _, extra := range vertexCapabilityProfileExtraIDs {
		for _, id := range extra {
			ids[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func vertexCapabilityProfileModelMappingIDs(profile string) ([]string, bool) {
	mapping, known := vertexCapabilityProfileModelMapping(profile)
	ids := make([]string, 0, len(mapping))
	for id := range mapping {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, known
}

func vertexCapabilityProfileModelMapping(profile string) (map[string]string, bool) {
	profile = strings.ToLower(strings.TrimSpace(profile))
	extra, ok := vertexCapabilityProfileExtraIDs[profile]
	out := vertexSharedModelMappingPreset()
	for _, id := range extra {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := out[id]; !exists {
			out[id] = id
		}
	}
	return out, ok
}

func vertexCapabilityProfileMappingsForOps() map[string]map[string]string {
	profiles := make(map[string]map[string]string, len(vertexCapabilityProfileExtraIDs))
	for profile := range vertexCapabilityProfileExtraIDs {
		mapping, _ := vertexCapabilityProfileModelMapping(profile)
		profiles[profile] = mapping
	}
	return profiles
}

func vertexModelMappingForAccount(account *Account) (map[string]string, bool) {
	return vertexCapabilityProfileModelMapping(account.VertexCapabilityProfile())
}

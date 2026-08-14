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

var vertexSharedModelMappingIDs = []string{
	"gemini-2.5-flash",
	"gemini-2.5-flash-lite",
	"gemini-3.5-flash-lite",
	"gemini-3.6-flash",
	"gemini-embedding-001",
	"veo-3.1-generate-001",
}

var vertexCapabilityProfileExtraIDs = map[string][]string{
	vertexCapabilityProfileCorePro: {
		"gemini-2.5-pro",
	},
	vertexCapabilityProfileCoreProImagenStandard: {
		"gemini-2.5-pro",
		"imagen-4.0-generate-001",
	},
	vertexCapabilityProfileCoreProImagenFastStandard: {
		"gemini-2.5-pro",
		"imagen-4.0-fast-generate-001",
		"imagen-4.0-generate-001",
	},
	vertexCapabilityProfileCoreImagenUltra: {
		"imagen-4.0-ultra-generate-001",
	},
}

// VertexCapabilityProfile returns the normalized ch41 capability selector.
// It never interprets account identity or name as capability evidence.
func (a *Account) VertexCapabilityProfile() string {
	if a == nil || a.Platform != PlatformNewAPI || a.ChannelType != newapiconstant.ChannelTypeVertexAi {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(a.GetCredential(VertexCapabilityProfileCredentialKey)))
}

func vertexSharedModelMappingPresetIDs() []string {
	return append([]string(nil), vertexSharedModelMappingIDs...)
}

func vertexCapabilityProfileModelMappingIDs(profile string) ([]string, bool) {
	profile = strings.ToLower(strings.TrimSpace(profile))
	extra, ok := vertexCapabilityProfileExtraIDs[profile]
	if !ok {
		ids := vertexSharedModelMappingPresetIDs()
		sort.Strings(ids)
		return ids, false
	}
	ids := append(vertexSharedModelMappingPresetIDs(), extra...)
	sort.Strings(ids)
	return ids, true
}

func vertexCapabilityProfileMappingsForOps() map[string]map[string]string {
	profiles := make(map[string]map[string]string, len(vertexCapabilityProfileExtraIDs))
	for profile := range vertexCapabilityProfileExtraIDs {
		ids, _ := vertexCapabilityProfileModelMappingIDs(profile)
		profiles[profile] = identityModelMapping(ids)
	}
	return profiles
}

func vertexModelMappingForAccount(account *Account) (map[string]string, bool) {
	ids, known := vertexCapabilityProfileModelMappingIDs(account.VertexCapabilityProfile())
	return identityModelMapping(ids), known
}

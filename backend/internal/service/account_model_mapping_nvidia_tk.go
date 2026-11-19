package service

import newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"

// Targets are NVIDIA API spellings; serving intent remains in the manifest.
// Floor is evidence-backed (direct upstream on account 138, 2026-11-19):
// moonshotai/kimi-k3 and z-ai/glm-5.3 return 410 EOL; z-ai/glm-5.3-flash stays
// listed but chat timed out; moonshotai/kimi-k3.1-pro returned HTTP 200.
// deepseek-v4-flash* remains 410 EOL / forbidden on NVIDIA.
var nvidiaBuildModelTargets = map[string]string{
	"kimi-k3.1-pro": "moonshotai/kimi-k3.1-pro",
}

// nvidiaBuildForbiddenModelMappingKeys strips withdrawn / unproven extras so
// apply-accounts converges live credentials to nvidiaBuildModelTargets exactly.
var nvidiaBuildForbiddenModelMappingKeys = []string{
	"deepseek-v4-flash",
	"deepseek-v4-flash-0731",
	"kimi-k3",
	"glm-5.3",
	"glm-5.3-flash",
}

func isNewAPINVIDIABuildAccount(account *Account) bool {
	return account != nil && account.Platform == PlatformNewAPI &&
		newapiintegration.IsNVIDIABuildBaseURL(account.ChannelType, account.GetBaseURL())
}

func nvidiaBuildModelMapping(account *Account) map[string]string {
	ids := NewAPIModelMappingPresetIDsForAccount(account)
	mapping := make(map[string]string, len(ids))
	for _, id := range ids {
		if target := nvidiaBuildModelTargets[id]; target != "" {
			mapping[id] = target
		}
	}
	return mapping
}

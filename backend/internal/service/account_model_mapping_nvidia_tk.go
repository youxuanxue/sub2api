package service

import newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"

// Targets are NVIDIA API spellings; serving intent remains in the manifest.
// Floor is evidence-backed (direct upstream chat 2026-09-30 on account 138):
// only these three wire ids returned HTTP 200. deepseek-v4-flash* is 410 EOL on
// NVIDIA; other catalog rows return Function-not-found 404 for this credential.
var nvidiaBuildModelTargets = map[string]string{
	"kimi-k3":       "moonshotai/kimi-k3",
	"glm-5.3":       "z-ai/glm-5.3",
	"glm-5.3-flash": "z-ai/glm-5.3-flash",
}

// nvidiaBuildForbiddenModelMappingKeys strips withdrawn / unproven extras so
// apply-accounts converges live credentials to nvidiaBuildModelTargets exactly.
var nvidiaBuildForbiddenModelMappingKeys = []string{
	"deepseek-v4-flash",
	"deepseek-v4-flash-0731",
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

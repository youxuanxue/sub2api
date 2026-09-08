package service

import newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"

// Targets are NVIDIA API spellings; serving intent remains in the manifest.
var nvidiaBuildModelTargets = map[string]string{
	"kimi-k3":           "moonshotai/kimi-k3",
	"deepseek-v4-flash": "deepseek-ai/deepseek-v4-flash-0731",
	"deepseek-v4-pro":   "deepseek-ai/deepseek-v4-pro-0813",
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

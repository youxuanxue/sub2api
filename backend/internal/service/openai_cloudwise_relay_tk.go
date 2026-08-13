package service

import "strings"

// openAICloudwiseRelayAllowedModelPrefixes is the single SSOT for which model
// families CloudWise MaaS relay accounts may serve. Adjust only here.
var openAICloudwiseRelayAllowedModelPrefixes = []string{
	"kimi-",
	"claude-",
	"glm-",
	"minimax-",
	"deepseek-",
}

func openAICloudwiseRelayWildcardModelMappingFloor() map[string]string {
	out := make(map[string]string, len(openAICloudwiseRelayAllowedModelPrefixes))
	for _, prefix := range openAICloudwiseRelayAllowedModelPrefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		pattern := prefix + "*"
		out[pattern] = pattern
	}
	return out
}

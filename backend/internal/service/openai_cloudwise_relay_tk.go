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

// openAICloudwiseRelayUpstreamModelID rewrites client model spellings to the exact
// wire ids CloudWise upstream accepts. Prod account #95 probe (2026-08-13):
// GET /v1/models lists MiniMax-M3; only that mixed-case id returns 200 on
// /v1/chat/completions — lowercase minimax-m3 returns 400 "not supported".
func openAICloudwiseRelayUpstreamModelID(modelID string) string {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	if !strings.HasPrefix(normalized, "minimax-") {
		return modelID
	}
	suffix := strings.TrimPrefix(normalized, "minimax-")
	if suffix == "" {
		return modelID
	}
	if suffix[0] >= 'a' && suffix[0] <= 'z' {
		suffix = strings.ToUpper(suffix[:1]) + suffix[1:]
	}
	return "MiniMax-" + suffix
}

func openAICloudwiseRelayProtocolProbeModel() string {
	return openAICloudwiseRelayUpstreamModelID("minimax-m3")
}

func applyOpenAICloudwiseRelayUpstreamModelID(account *Account, modelID string) string {
	if account == nil || !isCloudwiseRelayAccount(account) {
		return modelID
	}
	return openAICloudwiseRelayUpstreamModelID(modelID)
}

func isCloudwiseRelayAccount(account *Account) bool {
	if account == nil {
		return false
	}
	return isCloudwiseRelayBaseURL(account.GetCredential("base_url"))
}

func openAICloudwiseRelaySupportsRequestedModel(requestedModel string) bool {
	normalized := strings.ToLower(strings.TrimSpace(requestedModel))
	if normalized == "" {
		return false
	}
	for _, prefix := range openAICloudwiseRelayAllowedModelPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

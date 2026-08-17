package service

import (
	"net/url"
	"strings"
)

// openAICloudwiseRelayAllowedModelPrefixes is the single SSOT for which model
// families CloudWise MaaS relay accounts may serve. Adjust only here.
var openAICloudwiseRelayAllowedModelPrefixes = []string{
	"kimi-",
	"claude-",
	"glm-",
	"minimax-",
	"deepseek-",
}

// cloudwiseRelayCanonicalBaseURLs is the path CloudWise nginx actually serves.
// Host-only or /v1 spellings resolve to the same /api prefix; without it,
// /v1/chat/completions and /v1/responses return nginx/1.24.0 HTML 404.
var cloudwiseRelayCanonicalBaseURLs = map[string]string{
	"api.cloudwise.ai":    "https://api.cloudwise.ai/api",
	"api-us.cloudwise.ai": "https://api-us.cloudwise.ai/api",
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

func applyOpenAICloudwiseRelayUpstreamModelID(account *Account, modelID string) string {
	if account == nil || !account.IsOpenAICloudwiseRelay() {
		return modelID
	}
	return openAICloudwiseRelayUpstreamModelID(modelID)
}

func normalizeCloudwiseRelayBaseURL(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	canonical, ok := cloudwiseRelayCanonicalBaseURLs[strings.ToLower(parsed.Hostname())]
	if !ok {
		return "", false
	}
	switch strings.ToLower(strings.TrimRight(parsed.Path, "/")) {
	case "", "/api", "/v1", "/api/v1":
		return canonical, true
	default:
		return "", false
	}
}

func isCloudwiseRelayBaseURL(raw string) bool {
	_, ok := normalizeCloudwiseRelayBaseURL(raw)
	return ok
}

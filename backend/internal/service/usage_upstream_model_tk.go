package service

import "strings"

// preferUsageUpstreamModel chooses the model name stored on usage_logs.upstream_model.
//
// Protocol route facts carry Plan.ResolvedModel (account floor / schedule wire).
// Antigravity Gemini may remap the bare name to a thinking-tier variant at
// forward time (e.g. gemini-4-argon-medium → gemini-4-argon-high). In that case
// ForwardResult.UpstreamModel is the model actually sent upstream and must win
// over the plan floor so usage stays aligned with model_mapping_chain.
//
// For other disagreements the plan remains authoritative (protocol-routing SSOT).
func preferUsageUpstreamModel(forwarded, planned string) string {
	forwarded = strings.TrimSpace(forwarded)
	planned = strings.TrimSpace(planned)
	if forwarded == "" {
		return planned
	}
	if planned == "" || strings.EqualFold(forwarded, planned) {
		return forwarded
	}
	if sameGeminiThinkingFamily(forwarded, planned) {
		return forwarded
	}
	return planned
}

func sameGeminiThinkingFamily(a, b string) bool {
	a = strings.TrimSpace(strings.TrimPrefix(a, "models/"))
	b = strings.TrimSpace(strings.TrimPrefix(b, "models/"))
	if a == "" || b == "" {
		return false
	}
	aLower := strings.ToLower(a)
	bLower := strings.ToLower(b)
	if !strings.HasPrefix(aLower, "gemini-") || !strings.HasPrefix(bLower, "gemini-") {
		return false
	}
	return strings.EqualFold(stripGeminiThinkingVariantSuffix(a), stripGeminiThinkingVariantSuffix(b))
}

package service

import "strings"

func lastOpenAIModelSegment(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if strings.Contains(model, "/") {
		parts := strings.Split(model, "/")
		model = parts[len(parts)-1]
	}
	return strings.TrimSpace(model)
}

// isOpenAIGPT56Model accepts base names and supported GPT-5.6 variants.
func isOpenAIGPT56Model(model string) bool {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	if normalized == "gpt-5.6" {
		return true
	}
	if suffix, ok := strings.CutPrefix(normalized, "gpt-5.6-"); ok && (suffix == "max" || isKnownCodexModelSuffix(suffix)) {
		return true
	}
	for _, prefix := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if normalized == prefix || strings.HasPrefix(normalized, prefix+"-") {
			return true
		}
	}
	return false
}

// CanonicalizeOpenAICompatRoutingModel normalizes OpenAI-compat model ids for
// account selection, channel restriction, and negative-cache keys. Wire spellings
// such as gpt5.4-mini collapse to gpt-5.4-mini; legacy Codex ids route to the
// OAuth-served spark wire id; non-OpenAI ids pass through trimmed.
func CanonicalizeOpenAICompatRoutingModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}
	normalized := trimmed
	if canonical := canonicalizeOpenAIModelAliasSpelling(trimmed); canonical != "" {
		normalized = canonical
	}
	if alias := resolveOpenAICompatRoutingAlias(normalized); alias != "" {
		return alias
	}
	return normalized
}

func canonicalizeOpenAIModelAliasSpelling(model string) string {
	model = strings.ToLower(lastOpenAIModelSegment(model))
	if model == "" {
		return ""
	}

	normalized := strings.ReplaceAll(model, "_", "-")
	normalized = strings.Join(strings.Fields(normalized), "-")
	for strings.Contains(normalized, "--") {
		normalized = strings.ReplaceAll(normalized, "--", "-")
	}

	if strings.HasPrefix(normalized, "gpt5") {
		normalized = "gpt-5" + strings.TrimPrefix(normalized, "gpt5")
	}
	if !strings.HasPrefix(normalized, "gpt-") && !strings.Contains(normalized, "codex") {
		return ""
	}

	replacements := []struct {
		from string
		to   string
	}{
		{"gpt-5.4mini", "gpt-5.4-mini"},
		{"gpt-5.4nano", "gpt-5.4-nano"},
		{"gpt-5.3-codexspark", "gpt-5.3-codex-spark"},
		{"gpt-5.3codexspark", "gpt-5.3-codex-spark"},
		{"gpt-5.3codex", "gpt-5.3-codex"},
	}
	for _, replacement := range replacements {
		normalized = strings.ReplaceAll(normalized, replacement.from, replacement.to)
	}
	return normalized
}

// openAICompatRoutingAliases maps legacy client ids to the OAuth-served wire id
// without requiring per-account model_mapping keys.
var openAICompatRoutingAliases = map[string]string{
	"gpt-5":             "gpt-5.5",
	"gpt-5-chat":        "gpt-5.5",
	"gpt-5-chat-latest": "gpt-5.5",
	"gpt-5.5-pro":       "gpt-5.5",
	// Non-display compatibility alias: clients that type the bare official
	// family id should reach the live-proven Codex Spark wire id, without
	// advertising bare gpt-5.3 in the catalog.
	"gpt-5.3":             "gpt-5.3-codex-spark",
	"gpt-5.3-chat-latest": "gpt-5.3-codex-spark",
}

func resolveOpenAICompatRoutingAlias(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if target, ok := openAICompatRoutingAliases[model]; ok {
		return target
	}
	if target := normalizeKnownOpenAICodexModel(model); target != "" {
		return target
	}
	return model
}

func normalizeKnownOpenAICodexModel(model string) string {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	if normalized == "" {
		return ""
	}
	if mapped := getNormalizedCodexModel(normalized); mapped != "" {
		return mapped
	}
	if strings.HasSuffix(normalized, "-openai-compact") {
		if mapped := getNormalizedCodexModel(strings.TrimSuffix(normalized, "-openai-compact")); mapped != "" {
			return mapped
		}
	}

	switch {
	case strings.Contains(normalized, "gpt-5.6-sol"):
		return "gpt-5.6-sol"
	case strings.Contains(normalized, "gpt-5.6-terra"):
		return "gpt-5.6-terra"
	case strings.Contains(normalized, "gpt-5.6-luna"):
		return "gpt-5.6-luna"
	case normalized == "gpt-5.6":
		return "gpt-5.6-sol"
	case strings.HasPrefix(normalized, "gpt-5.6-"):
		suffix := strings.TrimPrefix(normalized, "gpt-5.6-")
		if suffix == "max" || isKnownCodexModelSuffix(suffix) {
			return "gpt-5.6-sol"
		}
		return ""
	case strings.Contains(normalized, "gpt-5.5-pro"):
		return "gpt-5.5"
	case strings.Contains(normalized, "gpt-5.5"):
		return "gpt-5.5"
	case strings.Contains(normalized, "gpt-5.4-mini"):
		return "gpt-5.4-mini"
	case strings.Contains(normalized, "gpt-5.4-nano"):
		return "gpt-5.4-nano"
	case strings.Contains(normalized, "gpt-5.4"):
		return "gpt-5.4"
	case strings.Contains(normalized, "gpt-5.2"):
		return "gpt-5.2"
	case strings.Contains(normalized, "gpt-5.3-codex-spark"):
		return "gpt-5.3-codex-spark"
	case strings.Contains(normalized, "gpt-5.3-codex"):
		return "gpt-5.3-codex-spark"
	case strings.Contains(normalized, "gpt-5-codex"):
		return "gpt-5.3-codex-spark"
	case normalized == "gpt-5.3":
		return "gpt-5.3-codex-spark"
	case strings.Contains(normalized, "codex"):
		return "gpt-5.3-codex-spark"
	case strings.Contains(normalized, "gpt-5-chat"):
		return "gpt-5.5"
	case normalized == "gpt-5":
		return "gpt-5.5"
	case strings.Contains(normalized, "gpt-5"):
		return "gpt-5.4"
	default:
		return ""
	}
}

// normalizeOpenAIBillingModel maps OpenAI/Codex wire ids to billing tier keys.
// Codex wire transform keeps ids such as gpt-5.6-chat-latest; billing collapses them to Sol/Terra/Luna.
func normalizeOpenAIBillingModel(model string) string {
	normalized := normalizeKnownOpenAICodexModel(model)
	if normalized == "" || !strings.Contains(normalized, "gpt-5.6") {
		return normalized
	}
	switch {
	case strings.Contains(normalized, "luna"):
		return "gpt-5.6-luna"
	case strings.Contains(normalized, "terra"):
		return "gpt-5.6-terra"
	case strings.Contains(normalized, "chat"), strings.Contains(normalized, "sol"), normalized == "gpt-5.6":
		return "gpt-5.6-sol"
	default:
		return "gpt-5.6-sol"
	}
}

func appendUsageBillingModelCandidate(candidates []string, seen map[string]struct{}, model string) []string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return candidates
	}
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
	}

	add(trimmed)
	if canonical := canonicalizeOpenAIModelAliasSpelling(trimmed); canonical != "" {
		add(canonical)
	}
	if normalized := normalizeOpenAIBillingModel(trimmed); normalized != "" {
		add(normalized)
	}
	return candidates
}

func usageBillingModelCandidates(primary string, alternates ...string) []string {
	seen := make(map[string]struct{}, 1+len(alternates))
	candidates := appendUsageBillingModelCandidate(nil, seen, primary)
	for _, alternate := range alternates {
		candidates = appendUsageBillingModelCandidate(candidates, seen, alternate)
	}
	return candidates
}

func firstUsageBillingModel(candidates []string) string {
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

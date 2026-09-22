package service

import (
	"encoding/json"
	"strings"
)

// Gemini clients may send a bare model id and put the requested reasoning
// depth in generationConfig.thinkingConfig. Antigravity's upstream catalog
// exposes the corresponding -low/-medium/-high/-tiered ids instead of the
// bare id, so resolve the wire id before forwarding the request.
//
// Contract (native-aligned):
//   - No thinkingConfig → leave bare→floor remap alone (3.8/3.7 lock high;
//     3.6 locks tiered).
//   - Explicit thinkingLevel / thinkingBudget → rewrite to the matching
//     wire tier when that tier is present in account model_mapping.
var geminiThinkingVariantSuffixes = []string{"-low", "-medium", "-high", "-tiered"}

const (
	geminiThinkingBudgetLowMax    = 1024
	geminiThinkingBudgetMediumMax = 8192
)

type geminiThinkingConfigProbe struct {
	GenerationConfig struct {
		ThinkingConfig *struct {
			ThinkingBudget *json.Number `json:"thinkingBudget"`
			ThinkingLevel  string       `json:"thinkingLevel"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig"`
}

func hasGeminiThinkingVariantSuffix(model string) bool {
	for _, suffix := range geminiThinkingVariantSuffixes {
		if strings.HasSuffix(model, suffix) {
			return true
		}
	}
	return false
}

func stripGeminiThinkingVariantSuffix(model string) string {
	for _, suffix := range geminiThinkingVariantSuffixes {
		if strings.HasSuffix(model, suffix) {
			return strings.TrimSuffix(model, suffix)
		}
	}
	return model
}

// geminiThinkingPreferenceFromBody returns the preferred AG thinking tier and
// whether the client explicitly set thinkingConfig (level and/or budget).
// Absent thinkingConfig → explicit=false so the bare floor remap stays in force.
func geminiThinkingPreferenceFromBody(body []byte) (level string, explicit bool) {
	if len(body) == 0 {
		return "", false
	}
	var probe geminiThinkingConfigProbe
	if err := json.Unmarshal(body, &probe); err != nil || probe.GenerationConfig.ThinkingConfig == nil {
		return "", false
	}
	tc := probe.GenerationConfig.ThinkingConfig
	switch strings.ToLower(strings.TrimSpace(tc.ThinkingLevel)) {
	case "low":
		return "low", true
	case "medium":
		return "medium", true
	case "high":
		return "high", true
	}
	if tc.ThinkingBudget == nil {
		// thinkingConfig present but neither level nor budget: leave the bare
		// floor remap alone (keeps gemini-3.6-flash → -tiered).
		return "", false
	}
	budget, err := tc.ThinkingBudget.Float64()
	if err != nil {
		return "high", true
	}
	switch {
	case budget < 0:
		return "high", true
	case budget <= geminiThinkingBudgetLowMax:
		return "low", true
	case budget <= geminiThinkingBudgetMediumMax:
		return "medium", true
	default:
		return "high", true
	}
}

// resolveGeminiThinkingVariant returns the mapped upstream model and whether
// an explicit thinkingConfig selected a wire tier. Bare requests without
// thinkingConfig leave the account floor remap untouched.
func resolveGeminiThinkingVariant(account *Account, requestedModel string, body []byte) (string, bool) {
	if account == nil {
		return "", false
	}
	model := strings.TrimSpace(strings.TrimPrefix(requestedModel, "models/"))
	if !strings.HasPrefix(model, "gemini-") || hasGeminiThinkingVariantSuffix(model) {
		return "", false
	}
	preferred, explicit := geminiThinkingPreferenceFromBody(body)
	if !explicit {
		return "", false
	}
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		return "", false
	}

	base := model
	if mapped, matched := resolveRequestedModelInMapping(mapping, model); matched {
		mapped = strings.TrimSpace(mapped)
		switch {
		case hasGeminiThinkingVariantSuffix(mapped):
			base = stripGeminiThinkingVariantSuffix(mapped)
		case mapped != model:
			// Non-tier remaps (e.g. image aliases) stay authoritative.
			return "", false
		}
	}

	order := []string{preferred}
	for _, level := range []string{"high", "medium", "low", "tiered"} {
		if level != preferred {
			order = append(order, level)
		}
	}
	for _, level := range order {
		candidate := base + "-" + level
		if mapped, matched := resolveRequestedModelInMapping(mapping, candidate); matched && strings.TrimSpace(mapped) != "" {
			return mapped, true
		}
	}
	return "", false
}

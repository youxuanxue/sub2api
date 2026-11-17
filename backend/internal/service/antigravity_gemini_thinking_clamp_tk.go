package service

import (
	"log/slog"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Antigravity Gemini native (/v1beta → ForwardGemini) thinkingBudget / maxOutputTokens
// safety. Aligns with new-api's "clamp what the client sent; never invent a large
// budget" spirit. Claude→AG TransformClaudeToGemini (64000 default) is untouched.

const (
	// Upstream Gemini thinking_budget legal domain is [-1, 65535]; -1 = dynamic.
	antigravityGeminiThinkingBudgetMin = -1
	antigravityGeminiThinkingBudgetMax = 65535

	// Argon family hard cap for generationConfig.maxOutputTokens. Chosen as
	// 8192 (not 16384) to stop the evidenced 65536 thinking runaway without
	// inventing a new default for non-thinking requests. Override via
	// antigravityGeminiMaxOutputTokensCapForModel.
	antigravityGeminiArgonMaxOutputTokensCap = 8192

	// When true, unset maxOutputTokens + present thinkingConfig on a capped
	// model injects the hard cap as a safety overlay. Flip to false to require
	// an explicit client maxOutputTokens (hard clamp of oversized values still
	// applies when the field is set).
	antigravityGeminiInjectMaxOutputTokensWhenThinking = true
)

const (
	thinkingConfigPath           = "generationConfig.thinkingConfig"
	thinkingBudgetPath           = "generationConfig.thinkingConfig.thinkingBudget"
	thinkingLevelPath            = "generationConfig.thinkingConfig.thinkingLevel"
	generationMaxOutputPath      = "generationConfig.maxOutputTokens"
	generationMaxOutputSnakePath = "generationConfig.max_output_tokens"
)

// normalizeAntigravityNativeGeminiRequest clamps thinkingBudget and applies the
// argon maxOutputTokens hard cap on the ForwardGemini native path only. Call
// before resolveGeminiThinkingVariant so illegal/oversized budgets cannot boost
// argon to -high via budget>8192.
func normalizeAntigravityNativeGeminiRequest(model string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	next := body
	next = normalizeAntigravityNativeThinkingBudget(model, next)
	next = applyAntigravityGeminiMaxOutputTokensCap(model, next)
	return next
}

func normalizeAntigravityNativeThinkingBudget(model string, body []byte) []byte {
	budgetNode := gjson.GetBytes(body, thinkingBudgetPath)
	levelNode := gjson.GetBytes(body, thinkingLevelPath)
	hasBudget := budgetNode.Exists() && budgetNode.Type != gjson.Null
	hasLevel := levelNode.Exists() && strings.TrimSpace(levelNode.String()) != ""
	if !hasBudget && !hasLevel {
		return body
	}

	levelModel := geminiThinkingParamForModel(model) == geminiThinkingParamLevel

	if levelModel {
		return normalizeAntigravityLevelModelThinking(model, body, hasBudget, hasLevel, budgetNode)
	}
	if !hasBudget {
		return body
	}
	return clampAntigravityThinkingBudgetValue(model, body, budgetNode)
}

// Level models (gemini-3+/argon): thinkingLevel and thinkingBudget are mutually
// exclusive upstream. Illegal/oversized budgets are dropped so they cannot be
// interpreted as budget>8192→high; legal budgets are rewritten to thinkingLevel.
func normalizeAntigravityLevelModelThinking(model string, body []byte, hasBudget, hasLevel bool, budgetNode gjson.Result) []byte {
	if hasLevel && hasBudget {
		next, err := sjson.DeleteBytes(body, thinkingBudgetPath)
		if err != nil {
			return body
		}
		logAntigravityGeminiThinkingRewrite(model, "drop_budget_keep_level", budgetNode.Raw, "")
		return next
	}
	if !hasBudget {
		return body
	}

	budget, ok := parseThinkingBudgetNumber(budgetNode)
	if !ok {
		next, err := sjson.DeleteBytes(body, thinkingBudgetPath)
		if err != nil {
			return body
		}
		logAntigravityGeminiThinkingRewrite(model, "drop_non_numeric_budget", budgetNode.Raw, "")
		return next
	}

	if budget < antigravityGeminiThinkingBudgetMin || budget > antigravityGeminiThinkingBudgetMax {
		// Illegal / 超大: drop; leave account floor mapping (argon → medium).
		next, err := sjson.DeleteBytes(body, thinkingBudgetPath)
		if err != nil {
			return body
		}
		logAntigravityGeminiThinkingRewrite(model, "drop_illegal_budget", strconv.FormatInt(budget, 10), "")
		return next
	}

	level := thinkingLevelFromClampedBudget(budget)
	next, err := sjson.SetBytes(body, thinkingLevelPath, level)
	if err != nil {
		return body
	}
	next, err = sjson.DeleteBytes(next, thinkingBudgetPath)
	if err != nil {
		return body
	}
	logAntigravityGeminiThinkingRewrite(model, "budget_to_level", strconv.FormatInt(budget, 10), level)
	return next
}

func clampAntigravityThinkingBudgetValue(model string, body []byte, budgetNode gjson.Result) []byte {
	budget, ok := parseThinkingBudgetNumber(budgetNode)
	if !ok {
		return body
	}
	clamped := budget
	if clamped < antigravityGeminiThinkingBudgetMin {
		clamped = antigravityGeminiThinkingBudgetMin
	}
	if clamped > antigravityGeminiThinkingBudgetMax {
		clamped = antigravityGeminiThinkingBudgetMax
	}
	if clamped == budget {
		return body
	}
	next, err := sjson.SetBytes(body, thinkingBudgetPath, clamped)
	if err != nil {
		return body
	}
	logAntigravityGeminiThinkingRewrite(model, "clamp_budget", strconv.FormatInt(budget, 10), strconv.FormatInt(clamped, 10))
	return next
}

func thinkingLevelFromClampedBudget(budget int64) string {
	switch {
	case budget < 0:
		return "high" // -1 dynamic
	case budget <= geminiThinkingBudgetLowMax:
		return "low"
	case budget <= geminiThinkingBudgetMediumMax:
		return "medium"
	default:
		return "high"
	}
}

func parseThinkingBudgetNumber(node gjson.Result) (int64, bool) {
	if !node.Exists() || node.Type == gjson.Null {
		return 0, false
	}
	if node.Type == gjson.Number {
		return node.Int(), true
	}
	// json.Number-as-string edge from some SDKs
	raw := strings.TrimSpace(node.String())
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// antigravityGeminiMaxOutputTokensCapForModel is the code SSOT for per-family
// hard caps. 0 means no cap. Group/platform overlays can later wrap this.
func antigravityGeminiMaxOutputTokensCapForModel(model string) int {
	name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(model, "models/")))
	if strings.HasPrefix(name, "gemini-4-argon") {
		return antigravityGeminiArgonMaxOutputTokensCap
	}
	return 0
}

func applyAntigravityGeminiMaxOutputTokensCap(model string, body []byte) []byte {
	cap := antigravityGeminiMaxOutputTokensCapForModel(model)
	if cap <= 0 {
		return body
	}

	path, node, ok := antigravityGeminiMaxOutputTokensField(body)
	if ok {
		current, parsed := parseThinkingBudgetNumber(node)
		if !parsed || current <= int64(cap) {
			return body
		}
		next, err := sjson.SetBytes(body, path, cap)
		if err != nil {
			return body
		}
		logAntigravityGeminiThinkingRewrite(model, "clamp_max_output_tokens", strconv.FormatInt(current, 10), strconv.Itoa(cap))
		return next
	}

	if !antigravityGeminiInjectMaxOutputTokensWhenThinking {
		return body
	}
	if !gjson.GetBytes(body, thinkingConfigPath).Exists() {
		return body
	}
	// Inject only when thinkingConfig is actionable (level or budget present),
	// not an empty thinkingConfig object.
	level := strings.TrimSpace(gjson.GetBytes(body, thinkingLevelPath).String())
	budgetExists := gjson.GetBytes(body, thinkingBudgetPath).Exists()
	if level == "" && !budgetExists {
		return body
	}
	next, err := sjson.SetBytes(body, generationMaxOutputPath, cap)
	if err != nil {
		return body
	}
	logAntigravityGeminiThinkingRewrite(model, "inject_max_output_tokens_cap", "", strconv.Itoa(cap))
	return next
}

func antigravityGeminiMaxOutputTokensField(body []byte) (path string, node gjson.Result, ok bool) {
	node = gjson.GetBytes(body, generationMaxOutputPath)
	if node.Exists() && node.Type != gjson.Null {
		return generationMaxOutputPath, node, true
	}
	node = gjson.GetBytes(body, generationMaxOutputSnakePath)
	if node.Exists() && node.Type != gjson.Null {
		return generationMaxOutputSnakePath, node, true
	}
	return "", gjson.Result{}, false
}

func logAntigravityGeminiThinkingRewrite(model, action, from, to string) {
	attrs := []any{
		"model", strings.TrimSpace(model),
		"action", action,
	}
	if from != "" {
		attrs = append(attrs, "from", from)
	}
	if to != "" {
		attrs = append(attrs, "to", to)
	}
	slog.Info("antigravity_gemini_native_thinking_rewritten", attrs...)
}

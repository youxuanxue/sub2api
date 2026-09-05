package service

import "strings"

// tkResolveFallbackOverlayPricing resolves TokenKey overlay / domestic-LLM /
// Codex fallback prices after the generic Claude/Gemini matchers. Returns nil
// when no TK branch matches (caller continues to Grok / final nil).
//
// Intentional closed matches (e.g. glm-4.5-x) also return nil; those IDs do not
// hit later Grok matchers, so behavior stays identical to an early return nil.
func (s *BillingService) tkResolveFallbackOverlayPricing(modelLower string) *ModelPricing {
	if s == nil {
		return nil
	}

	// DeepSeek V4 系列：仅匹配已知 V4 Pro/Flash 与官方兼容别名
	// （deepseek-chat / deepseek-reasoner → V4 Flash），未知 deepseek-* 型号不回退，避免误计价。
	if strings.Contains(modelLower, "deepseek-v4-flash") {
		return tkOverlayModelPricing("deepseek-v4-flash")
	}
	if strings.Contains(modelLower, "deepseek-v4-pro") {
		return tkOverlayModelPricing("deepseek-v4-pro")
	}
	if strings.Contains(modelLower, "deepseek-chat") || strings.Contains(modelLower, "deepseek-reasoner") {
		return tkOverlayModelPricing("deepseek-v4-flash")
	}

	// ---- 国产 LLM 兜底匹配 ----
	// 匹配策略：长 key 优先（具体模型 → 系列 / 厂商），未知型号不回退以避免误计价。
	// 与 DeepSeek 一样采用"白名单"语义：未在本表命中的国产模型 alias 一律不返回兜底价。

	// 智谱 GLM：定价源只用 BigModel 官方 pricing 页；可服务路径仍是 Qwen/DashScope 池。
	// 匹配顺序：先判别最高 tier，再依次降级。
	if canonical := normalizeGLMVolcengineDatedModelID(modelLower); canonical != "" {
		if pricing := tkOverlayModelPricing(canonical); pricing != nil {
			return pricing
		}
	}
	if strings.Contains(modelLower, "glm-5.3") {
		return tkOverlayModelPricing("glm-5.3")
	}
	if strings.Contains(modelLower, "glm-5.2") {
		return tkOverlayModelPricing("glm-5.2")
	}
	if strings.Contains(modelLower, "glm-5.1") {
		return tkOverlayModelPricing("glm-5.1")
	}
	if strings.Contains(modelLower, "glm-5-turbo") || strings.Contains(modelLower, "glm-5turbo") {
		return tkOverlayModelPricing("glm-5-turbo")
	}
	if strings.Contains(modelLower, "glm-5") {
		return tkOverlayModelPricing("glm-5")
	}
	if strings.Contains(modelLower, "glm-4.7-flashx") {
		return tkOverlayModelPricing("glm-4.7-flashx")
	}
	if strings.Contains(modelLower, "glm-4.7-flash") {
		return s.fallbackPrices["glm-4.7-flash"]
	}
	if strings.Contains(modelLower, "glm-4.7") {
		return tkOverlayModelPricing("glm-4.7")
	}
	if strings.Contains(modelLower, "glm-4.6") {
		return tkOverlayModelPricing("glm-4.6")
	}
	if strings.Contains(modelLower, "glm-4.5-flash") {
		return s.fallbackPrices["glm-4.5-flash"]
	}
	if strings.Contains(modelLower, "glm-4.5-x") || strings.Contains(modelLower, "glm-4.5x") ||
		strings.Contains(modelLower, "glm-4.5-airx") || strings.Contains(modelLower, "glm-4.5airx") {
		return nil
	}
	if strings.Contains(modelLower, "glm-4.5-air") || strings.Contains(modelLower, "glm-4.5air") {
		return tkOverlayModelPricing("glm-4.5-air")
	}
	if strings.Contains(modelLower, "glm-4.5") {
		return tkOverlayModelPricing("glm-4.5")
	}
	if strings.Contains(modelLower, "glm-4-32b") {
		return s.fallbackPrices["glm-4-32b-0414-128k"]
	}

	// 月之暗面 Kimi（kimi-k3 / k3 / k3-256k / kimi-k2.6 / kimi-for-coding / kimi-k2.5 / kimi-k2-thinking / kimi-k2）
	// K2-0905 / K2-0711 官方未保留定价，不进入 fallback。
	// K3 规则置于 K2 前：API Platform 仅官方 kimi-k3（及 / 路径后缀）；
	// Code bare aliases 仅精确 k3 / k3-256k 或 /k3|/k3-256k 后缀，避免 kimi-k30 等未知型号误命中。
	// 注意：kimi-k3[1m] 是 Claude Code 上下文选择语法，不是 Kimi API 模型 ID，不进入 fallback。
	if strings.Contains(modelLower, "kimi-for-coding") {
		return s.fallbackPrices["kimi-for-coding"]
	}
	if modelLower == "kimi-k3" || strings.HasSuffix(modelLower, "/kimi-k3") ||
		modelLower == "k3" || modelLower == "k3-256k" ||
		strings.HasSuffix(modelLower, "/k3") || strings.HasSuffix(modelLower, "/k3-256k") {
		return s.fallbackPrices["kimi-k3"]
	}
	if strings.Contains(modelLower, "kimi-k2.6") || strings.Contains(modelLower, "kimi-k2-6") {
		return s.fallbackPrices["kimi-k2.6"]
	}
	if strings.Contains(modelLower, "kimi-k2.5") || strings.Contains(modelLower, "kimi-k2-5") {
		return s.fallbackPrices["kimi-k2.5"]
	}
	if strings.Contains(modelLower, "kimi-k2-thinking") || strings.Contains(modelLower, "kimi-k2-thinking-") {
		return s.fallbackPrices["kimi-k2-thinking"]
	}
	if strings.Contains(modelLower, "kimi-k2") || strings.Contains(modelLower, "kimi/k2") {
		return s.fallbackPrices["kimi-k2"]
	}

	// MiniMax M 系列（M3 / M2.7 / M2.5 / M2.1 / M2；含 highspeed 变体）
	if strings.Contains(modelLower, "minimax-m3") {
		return s.fallbackPrices["minimax-m3"]
	}
	if strings.Contains(modelLower, "minimax-m2.7-highspeed") || strings.Contains(modelLower, "minimax-m2-7-highspeed") {
		return s.fallbackPrices["minimax-m2.7-highspeed"]
	}
	if strings.Contains(modelLower, "minimax-m2.7") || strings.Contains(modelLower, "minimax-m2-7") {
		return s.fallbackPrices["minimax-m2.7"]
	}
	if strings.Contains(modelLower, "minimax-m2.5") || strings.Contains(modelLower, "minimax-m2-5") {
		return s.fallbackPrices["minimax-m2.5"]
	}
	if strings.Contains(modelLower, "minimax-m2.1") || strings.Contains(modelLower, "minimax-m2-1") {
		return s.fallbackPrices["minimax-m2.1"]
	}
	if strings.Contains(modelLower, "minimax-m2") || strings.Contains(modelLower, "minimax-m-2") {
		return s.fallbackPrices["minimax-m2"]
	}

	// 火山方舟 豆包 Embedding（多模态向量化）。
	// most-specific-first：放在未来任何 doubao-embedding / doubao 宽匹配之前。
	// 覆盖带版本后缀的别名（如 doubao-embedding-vision-251215）。
	if strings.Contains(modelLower, "doubao-embedding-vision") {
		return s.fallbackPrices["doubao-embedding-vision"]
	}

	// OpenAI（GPT-5 / Codex 族）：仅匹配已知型号，避免未知 OpenAI 型号误计价。
	if normalized := normalizeOpenAIBillingModel(modelLower); normalized != "" {
		switch normalized {
		case "gpt-5.6-sol":
			return s.fallbackPrices["gpt-5.6-sol"]
		case "gpt-5.6-terra":
			return s.fallbackPrices["gpt-5.6-terra"]
		case "gpt-5.6-luna":
			return s.fallbackPrices["gpt-5.6-luna"]
		case "gpt-5.5-pro":
			return s.fallbackPrices["gpt-5.5-pro"]
		case "gpt-5.5":
			return s.fallbackPrices["gpt-5.5"]
		case "gpt-5.4-mini":
			return s.fallbackPrices["gpt-5.4-mini"]
		case "gpt-5.4-nano":
			return s.fallbackPrices["gpt-5.4-nano"]
		case "gpt-5.4":
			return s.fallbackPrices["gpt-5.4"]
		case "gpt-5.2":
			return s.fallbackPrices["gpt-5.2"]
		case "gpt-5.3-codex", "gpt-5.3-codex-spark":
			return s.fallbackPrices["gpt-5.3-codex"]
		}
	}
	// Unknown chat-shaped gpt-* variants use the gpt-5.4 registry owner and emit
	// served_at_fallback for convergence. Non-chat GPT modes stay excluded so an
	// image/audio/realtime request cannot inherit token pricing accidentally.
	if strings.Contains(modelLower, "gpt") &&
		!strings.Contains(modelLower, "image") && !strings.Contains(modelLower, "audio") &&
		!strings.Contains(modelLower, "realtime") && !strings.Contains(modelLower, "transcribe") &&
		!strings.Contains(modelLower, "-tts") {
		return s.fallbackPrices["gpt-5.4"]
	}

	return nil
}

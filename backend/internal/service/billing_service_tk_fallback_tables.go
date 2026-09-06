package service

import "strings"

// tkInitFallbackPricingTables populates TokenKey-extended hardcoded fallback
// price cards (Fable / Gemini / GPT / DeepSeek / GLM / domestic LLM / Doubao).
// Claude classic Opus/Sonnet/Haiku and xAI Grok cards stay in initFallbackPricing.
func (s *BillingService) tkInitFallbackPricingTables() {
	// Claude Fable 5 (Opus 之上的新档；官方 $10/$50 per MTok)。
	s.fallbackPrices["claude-fable-5"] = &ModelPricing{
		InputPricePerToken:         10e-6,
		OutputPricePerToken:        50e-6,
		CacheCreationPricePerToken: 12.5e-6,
		CacheReadPricePerToken:     1.0e-6,
		SupportsCacheBreakdown:     false,
	}

	// Gemini 3.1 Pro
	s.fallbackPrices["gemini-3.1-pro"] = &ModelPricing{
		InputPricePerToken:         2e-6,   // $2 per MTok
		OutputPricePerToken:        12e-6,  // $12 per MTok
		CacheCreationPricePerToken: 2e-6,   // $2 per MTok
		CacheReadPricePerToken:     0.2e-6, // $0.20 per MTok
		SupportsCacheBreakdown:     false,
	}

	// Gemini 3.6 Flash (Google AI pricing: $1.50 input / $7.50 output /
	// $0.15 cached input per MTok). Antigravity's -high/-low/-medium/-tiered
	// aliases are matched below so unavailable remote pricing never records
	// token-bearing requests at $0.
	s.fallbackPrices["gemini-3.6-flash"] = &ModelPricing{
		InputPricePerToken:     1.5e-6,
		OutputPricePerToken:    7.5e-6,
		CacheReadPricePerToken: 0.15e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["gemini-2.5-flash"] = &ModelPricing{
		InputPricePerToken:     3e-7,
		OutputPricePerToken:    2.5e-6,
		CacheReadPricePerToken: 3e-8,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["gemini-2.5-pro"] = &ModelPricing{
		InputPricePerToken:     1.25e-6,
		OutputPricePerToken:    1e-5,
		CacheReadPricePerToken: 1.25e-7,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["gemini-2.5-flash-lite"] = &ModelPricing{
		InputPricePerToken:     1e-7,
		OutputPricePerToken:    4e-7,
		CacheReadPricePerToken: 1e-8,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["gemini-3.7-flash"] = &ModelPricing{
		InputPricePerToken:     0.75e-6,
		OutputPricePerToken:    3.75e-6,
		CacheReadPricePerToken: 0.075e-6,
		SupportsCacheBreakdown: false,
	}

	// OpenAI GPT-5.4（业务指定价格）
	s.fallbackPrices["gpt-5.4"] = &ModelPricing{
		InputPricePerToken:             2.5e-6,  // $2.5 per MTok
		InputPricePerTokenPriority:     5e-6,    // $5 per MTok
		OutputPricePerToken:            15e-6,   // $15 per MTok
		OutputPricePerTokenPriority:    30e-6,   // $30 per MTok
		CacheCreationPricePerToken:     2.5e-6,  // $2.5 per MTok
		CacheReadPricePerToken:         0.25e-6, // $0.25 per MTok
		CacheReadPricePerTokenPriority: 0.5e-6,  // $0.5 per MTok
		SupportsCacheBreakdown:         false,
		LongContextInputThreshold:      openAIGPT54LongContextInputThreshold,
		LongContextInputMultiplier:     openAIGPT54LongContextInputMultiplier,
		LongContextOutputMultiplier:    openAIGPT54LongContextOutputMultiplier,
	}
	// OpenAI GPT-5.5 官方价格；Fast 为标准价 2.5 倍。
	// Source: https://platform.openai.com/docs/pricing
	s.fallbackPrices["gpt-5.5"] = pricingWithPriorityMultiplier(&ModelPricing{
		InputPricePerToken:  5e-6,
		OutputPricePerToken: 30e-6,
		// 官方未列独立 cache-write 价；内部出现 cache creation token 时按输入价兜底。
		CacheCreationPricePerToken:  5e-6,
		CacheReadPricePerToken:      0.5e-6,
		SupportsCacheBreakdown:      false,
		LongContextInputThreshold:   openAIGPT54LongContextInputThreshold,
		LongContextInputMultiplier:  openAIGPT54LongContextInputMultiplier,
		LongContextOutputMultiplier: openAIGPT54LongContextOutputMultiplier,
	}, 2.5)
	// GPT-5.5 Pro 当前不提供 Fast；保留标准、Flex 和长上下文 fallback 价格。
	s.fallbackPrices["gpt-5.5-pro"] = &ModelPricing{
		InputPricePerToken:  30e-6,
		OutputPricePerToken: 180e-6,
		// 官方未列独立 cached-input/cache-write 价；内部出现对应 token 时按输入价兜底。
		CacheCreationPricePerToken:  30e-6,
		CacheReadPricePerToken:      30e-6,
		SupportsCacheBreakdown:      false,
		LongContextInputThreshold:   openAIGPT54LongContextInputThreshold,
		LongContextInputMultiplier:  openAIGPT54LongContextInputMultiplier,
		LongContextOutputMultiplier: openAIGPT54LongContextOutputMultiplier,
	}

	// OpenAI GPT-5.6 官方价格（USD/token）。缓存写入为输入价的 1.25 倍。
	s.fallbackPrices["gpt-5.6-sol"] = &ModelPricing{
		InputPricePerToken:                 5e-6,
		InputPricePerTokenPriority:         10e-6,
		OutputPricePerToken:                30e-6,
		OutputPricePerTokenPriority:        60e-6,
		CacheCreationPricePerToken:         6.25e-6,
		CacheCreationPricePerTokenPriority: 12.5e-6,
		CacheReadPricePerToken:             0.5e-6,
		CacheReadPricePerTokenPriority:     1e-6,
		LongContextInputThreshold:          openAIGPT54LongContextInputThreshold,
		LongContextInputMultiplier:         openAIGPT54LongContextInputMultiplier,
		LongContextOutputMultiplier:        openAIGPT54LongContextOutputMultiplier,
	}
	s.fallbackPrices["gpt-5.6-terra"] = &ModelPricing{
		InputPricePerToken:                 2e-6,
		InputPricePerTokenPriority:         4e-6,
		OutputPricePerToken:                12e-6,
		OutputPricePerTokenPriority:        24e-6,
		CacheCreationPricePerToken:         2.5e-6,
		CacheCreationPricePerTokenPriority: 5e-6,
		CacheReadPricePerToken:             0.2e-6,
		CacheReadPricePerTokenPriority:     0.4e-6,
		LongContextInputThreshold:          openAIGPT54LongContextInputThreshold,
		LongContextInputMultiplier:         openAIGPT54LongContextInputMultiplier,
		LongContextOutputMultiplier:        openAIGPT54LongContextOutputMultiplier,
	}
	s.fallbackPrices["gpt-5.6-luna"] = &ModelPricing{
		InputPricePerToken:                 0.2e-6,
		InputPricePerTokenPriority:         0.4e-6,
		OutputPricePerToken:                1.2e-6,
		OutputPricePerTokenPriority:        2.4e-6,
		CacheCreationPricePerToken:         0.25e-6,
		CacheCreationPricePerTokenPriority: 0.5e-6,
		CacheReadPricePerToken:             0.02e-6,
		CacheReadPricePerTokenPriority:     0.04e-6,
		LongContextInputThreshold:          openAIGPT54LongContextInputThreshold,
		LongContextInputMultiplier:         openAIGPT54LongContextInputMultiplier,
		LongContextOutputMultiplier:        openAIGPT54LongContextOutputMultiplier,
	}

	s.fallbackPrices["gpt-5.4-mini"] = &ModelPricing{
		InputPricePerToken:     7.5e-7,
		OutputPricePerToken:    4.5e-6,
		CacheReadPricePerToken: 7.5e-8,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["gpt-5.4-nano"] = &ModelPricing{
		InputPricePerToken:     2e-7,
		OutputPricePerToken:    1.25e-6,
		CacheReadPricePerToken: 2e-8,
		SupportsCacheBreakdown: false,
	}
	// OpenAI GPT-5.2（本地兜底）
	s.fallbackPrices["gpt-5.2"] = &ModelPricing{
		InputPricePerToken:             1.75e-6,
		InputPricePerTokenPriority:     3.5e-6,
		OutputPricePerToken:            14e-6,
		OutputPricePerTokenPriority:    28e-6,
		CacheCreationPricePerToken:     1.75e-6,
		CacheReadPricePerToken:         0.175e-6,
		CacheReadPricePerTokenPriority: 0.35e-6,
		SupportsCacheBreakdown:         false,
	}
	// Codex 族兜底统一按 GPT-5.3 Codex 价格计费
	s.fallbackPrices["gpt-5.3-codex"] = &ModelPricing{
		InputPricePerToken:             1.5e-6, // $1.5 per MTok
		InputPricePerTokenPriority:     3e-6,   // $3 per MTok
		OutputPricePerToken:            12e-6,  // $12 per MTok
		OutputPricePerTokenPriority:    24e-6,  // $24 per MTok
		CacheCreationPricePerToken:     1.5e-6, // $1.5 per MTok
		CacheReadPricePerToken:         0.15e-6,
		CacheReadPricePerTokenPriority: 0.3e-6,
		SupportsCacheBreakdown:         false,
	}

	// ============================================================
	// 国产 LLM 兜底定价（数据源：各家官方定价页/USD 口径）
	// 顺序：DeepSeek → 智谱 GLM → 月之暗面 Kimi → MiniMax
	// 覆盖逻辑见同文件 getFallbackPricing()
	// ============================================================

	// ---- DeepSeek V4 系列 ----
	// Source: https://api-docs.deepseek.com/quick_start/pricing
	// （deepseek-chat / deepseek-reasoner 为 deepseek-v4-flash 的兼容别名，2026/07/24 弃用）
	s.fallbackPrices["deepseek-v4-pro"] = &ModelPricing{
		InputPricePerToken:     4.35e-7,  // $0.435 per MTok (cache miss)
		OutputPricePerToken:    8.7e-7,   // $0.87 per MTok
		CacheReadPricePerToken: 3.625e-9, // $0.003625 per MTok (cache hit)
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["deepseek-v4-flash"] = &ModelPricing{
		InputPricePerToken:     1.4e-7, // $0.14 per MTok (cache miss)
		OutputPricePerToken:    2.8e-7, // $0.28 per MTok
		CacheReadPricePerToken: 2.8e-9, // $0.0028 per MTok (cache hit)
		SupportsCacheBreakdown: false,
	}

	// ---- 智谱 GLM（Z.AI）----
	// Source: https://docs.z.ai/guides/overview/pricing (USD per 1M tokens)
	// 注意：CacheReadPricePerToken 即"缓存命中"价格，CacheCreationPricePerToken 留空（智谱未公开写入价，按 0 处理）。
	// GLM-4.6 与 GLM-4.5 在 z.ai 国际版上定价一致；GLM-4.5 国内按 ¥0.8/¥2，汇率换算后约 $0.112/$0.28，与国际版 $0.6/$2.2 不同，本分支采用国际版 USD 口径与现有 Claude/GPT 一致。
	// GLM-5.2 与 GLM-5.1 在 z.ai 上同价。
	s.fallbackPrices["glm-5.2"] = &ModelPricing{
		InputPricePerToken:     1.4e-6, // $1.40 per MTok
		OutputPricePerToken:    4.4e-6, // $4.40 per MTok
		CacheReadPricePerToken: 0.26e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-5.1"] = &ModelPricing{
		InputPricePerToken:     1.4e-6, // $1.40 per MTok
		OutputPricePerToken:    4.4e-6, // $4.40 per MTok
		CacheReadPricePerToken: 0.26e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-5"] = &ModelPricing{
		InputPricePerToken:     1e-6, // $1.00 per MTok
		OutputPricePerToken:    3.2e-6,
		CacheReadPricePerToken: 0.2e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-5-turbo"] = &ModelPricing{
		InputPricePerToken:     1.2e-6,
		OutputPricePerToken:    4e-6,
		CacheReadPricePerToken: 0.24e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-4.7"] = &ModelPricing{
		InputPricePerToken:     0.6e-6, // $0.60 per MTok
		OutputPricePerToken:    2.2e-6,
		CacheReadPricePerToken: 0.11e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-4.7-flashx"] = &ModelPricing{
		InputPricePerToken:     0.07e-6, // $0.07 per MTok
		OutputPricePerToken:    0.4e-6,
		CacheReadPricePerToken: 0.01e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-4.6"] = &ModelPricing{
		InputPricePerToken:     0.6e-6, // $0.60 per MTok
		OutputPricePerToken:    2.2e-6,
		CacheReadPricePerToken: 0.11e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-4.5"] = &ModelPricing{
		InputPricePerToken:     0.6e-6, // $0.60 per MTok
		OutputPricePerToken:    2.2e-6,
		CacheReadPricePerToken: 0.11e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-4.5-x"] = &ModelPricing{
		InputPricePerToken:     2.2e-6, // $2.20 per MTok
		OutputPricePerToken:    8.9e-6,
		CacheReadPricePerToken: 0.45e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-4.5-air"] = &ModelPricing{
		InputPricePerToken:     0.2e-6, // $0.20 per MTok
		OutputPricePerToken:    1.1e-6,
		CacheReadPricePerToken: 0.03e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-4.5-airx"] = &ModelPricing{
		InputPricePerToken:     1.1e-6,
		OutputPricePerToken:    4.5e-6,
		CacheReadPricePerToken: 0.22e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-4-32b-0414-128k"] = &ModelPricing{
		InputPricePerToken:     0.1e-6, // $0.10 per MTok
		OutputPricePerToken:    0.1e-6,
		SupportsCacheBreakdown: false,
	}
	// GLM-4.5-Flash / GLM-4.7-Flash 在 z.ai 上为 Free，保留 zero-cost entry 防止未知 alias 误计费。
	s.fallbackPrices["glm-4.5-flash"] = &ModelPricing{
		InputPricePerToken:     0,
		OutputPricePerToken:    0,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["glm-4.7-flash"] = &ModelPricing{
		InputPricePerToken:     0,
		OutputPricePerToken:    0,
		SupportsCacheBreakdown: false,
	}

	// ---- 月之暗面 Kimi（K 系列）----
	// Source: https://platform.moonshot.cn/docs/pricing/overview (元/百万 tokens 口径)
	//       交叉验证：https://www.tmtpost.com/7961404.html (USD 口径)
	// Moonshot V1 (¥2/¥5/¥10 多 tier) 公开页未直接标注 USD 价，本分支不覆盖，避免误计价。
	// K2-0905 / K2-0711 官方页面未保留定价，不覆盖。
	// Kimi K3 国际站 USD 价目：https://platform.kimi.ai/docs/pricing/chat-k3.md
	// Kimi Code bare aliases（k3 / k3-256k）官方无按 token 价目；复用 API Platform
	// kimi-k3 档位作代理计费 fallback（同 kimi-for-coding 对 K2.6 的处理口径）。
	s.fallbackPrices["kimi-k3"] = &ModelPricing{
		InputPricePerToken:     3e-6,    // $3.00 per MTok (cache miss)
		OutputPricePerToken:    15e-6,   // $15.00 per MTok
		CacheReadPricePerToken: 0.30e-6, // $0.30 per MTok (cache hit)
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["kimi-k2.6"] = &ModelPricing{
		InputPricePerToken:     0.95e-6, // $0.95 per MTok (cache miss)
		OutputPricePerToken:    4e-6,    // $4.00 per MTok
		CacheReadPricePerToken: 0.15e-6, // $0.15 per MTok (cache hit, ¥1.10)
		SupportsCacheBreakdown: false,
	}
	// kimi-for-coding 走 Kimi Coding endpoint，按当前 K2.6 coding 档位兜底计费。
	s.fallbackPrices["kimi-for-coding"] = &ModelPricing{
		InputPricePerToken:     0.95e-6,
		OutputPricePerToken:    4e-6,
		CacheReadPricePerToken: 0.15e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["kimi-k2.5"] = &ModelPricing{
		InputPricePerToken:     0.60e-6, // $0.60 per MTok
		OutputPricePerToken:    3e-6,    // $3.00 per MTok
		CacheReadPricePerToken: 0.098e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["kimi-k2-thinking"] = &ModelPricing{
		InputPricePerToken:     tkCNYPerMTokToUSDPerToken(4),
		OutputPricePerToken:    tkCNYPerMTokToUSDPerToken(16),
		CacheReadPricePerToken: tkCNYPerMTokToUSDPerToken(1),
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["kimi-k2"] = &ModelPricing{
		InputPricePerToken:     tkCNYPerMTokToUSDPerToken(4),
		OutputPricePerToken:    tkCNYPerMTokToUSDPerToken(16),
		CacheReadPricePerToken: tkCNYPerMTokToUSDPerToken(1),
		SupportsCacheBreakdown: false,
	}

	// ---- MiniMax M 系列 ----
	// Source: https://platform.minimax.io/docs/guides/pricing-paygo
	// 注意：MiniMax M3 在 >512K context 时价格翻倍，本兜底采用 ≤512K 标准 tier（保守口径，对用户有利）。
	// 如需支持长上下文 multiplier，可后续参考 GPT-5.4 模式扩展 LongContextXxx 字段。
	s.fallbackPrices["minimax-m3"] = &ModelPricing{
		InputPricePerToken:     0.60e-6, // $0.60 per MTok (≤512K standard tier, 含 50% 永久折扣前原价 $1.20)
		OutputPricePerToken:    2.40e-6,
		CacheReadPricePerToken: 0.12e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["minimax-m2.7"] = &ModelPricing{
		InputPricePerToken:     0.30e-6, // $0.30 per MTok
		OutputPricePerToken:    1.20e-6,
		CacheReadPricePerToken: 0.06e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["minimax-m2.7-highspeed"] = &ModelPricing{
		InputPricePerToken:     0.60e-6,
		OutputPricePerToken:    2.40e-6,
		CacheReadPricePerToken: 0.06e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["minimax-m2.5"] = &ModelPricing{
		InputPricePerToken:     0.30e-6,
		OutputPricePerToken:    1.20e-6,
		CacheReadPricePerToken: 0.03e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["minimax-m2.1"] = &ModelPricing{
		InputPricePerToken:     0.30e-6,
		OutputPricePerToken:    1.20e-6,
		CacheReadPricePerToken: 0.03e-6,
		SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["minimax-m2"] = &ModelPricing{
		InputPricePerToken:     0.30e-6,
		OutputPricePerToken:    1.20e-6,
		CacheReadPricePerToken: 0.03e-6,
		SupportsCacheBreakdown: false,
	}

	// ---- 火山方舟 豆包 Embedding（多模态向量化）----
	// doubao-embedding-vision 图文向量化：上游 usage 回传 prompt_tokens_details.{text_tokens,image_tokens}，
	// 按量付费官方价 文本 ¥0.7/MTok、图片 ¥1.8/MTok；汇率口径 ÷7.14（与本表其他国产模型一致，¥1≈$0.14）。
	// embedding 无 output，OutputPricePerToken 置 0。
	s.fallbackPrices["doubao-embedding-vision"] = &ModelPricing{
		InputPricePerToken:      tkCNYPerMTokToUSDPerToken(0.7),
		ImageInputPricePerToken: tkCNYPerMTokToUSDPerToken(1.8),
		OutputPricePerToken:     0,
		SupportsCacheBreakdown:  false,
	}

}

// tkResolveFallbackTableFamilyPricing resolves Fable / Claude unknown-floor /
// Gemini family fallback matches that sit between classic Claude matchers and
// the domestic/GPT overlay (billing_service_tk_fallback_overlay.go).
// Order is load-bearing: Fable must beat the Claude sonnet floor.
func (s *BillingService) tkResolveFallbackTableFamilyPricing(modelLower string) *ModelPricing {
	if s == nil {
		return nil
	}

	// Claude Fable 5（Opus 之上的新档）必须先于下面的 claude 兜底命中，
	// 否则会被误判为 sonnet（$3/$15）严重少收费。
	if strings.Contains(modelLower, "fable") {
		return s.fallbackPrices["claude-fable-5"]
	}
	// Claude 未知型号统一回退到 Sonnet，避免计费中断。
	if strings.Contains(modelLower, "claude") {
		return s.fallbackPrices["claude-sonnet-4"]
	}
	if strings.Contains(modelLower, "gemini-3.1-pro") || strings.Contains(modelLower, "gemini-3-1-pro") {
		return s.fallbackPrices["gemini-3.1-pro"]
	}
	if strings.Contains(modelLower, "gemini-3.7-flash") || strings.Contains(modelLower, "gemini-3-7-flash") {
		return s.fallbackPrices["gemini-3.7-flash"]
	}
	if strings.Contains(modelLower, "gemini-3.6-flash") || strings.Contains(modelLower, "gemini-3-6-flash") {
		return s.fallbackPrices["gemini-3.6-flash"]
	}
	if strings.Contains(modelLower, "gemini") {
		if strings.Contains(modelLower, "pro") {
			return s.fallbackPrices["gemini-2.5-pro"]
		}
		if strings.Contains(modelLower, "flash-lite") || strings.Contains(modelLower, "flash-8b") {
			return s.fallbackPrices["gemini-2.5-flash-lite"]
		}
		return s.fallbackPrices["gemini-2.5-flash"]
	}

	return nil
}

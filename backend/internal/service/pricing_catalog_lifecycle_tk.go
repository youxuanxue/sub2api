package service

import "strings"

// Recommendation withdrawals are independent of reachability: a successful
// probe or a reseller alias must not advertise an officially sunsetting ID
// again. These facts affect presentation only, never request or billing gates.
type catalogModelWithdrawal struct {
	Source   string
	Sunset   string
	ModelIDs []string
}

var catalogModelWithdrawals = []catalogModelWithdrawal{
	{
		Source: "https://www.aliyun.com/notice/118345",
		Sunset: "2026-10-10",
		ModelIDs: []string{
			"qwen3-8b", "qwen3-14b", "qwen3-32b", "qwen3-235b-a22b",
			"glm-4.6", "glm-4.7", "deepseek-v3.2",
		},
	},
	{
		Source:   "https://www.aliyun.com/notice/118434",
		Sunset:   "2026-10-10",
		ModelIDs: []string{"glm-4.5", "glm-4.5-air", "deepseek-v4-flash"},
	},
	{
		Source: "https://cloud.baidu.com/doc/qianfan/s/zmh4stou3",
		Sunset: "2026-09-29",
		ModelIDs: []string{
			"glm-5", "kimi-k2.6", "deepseek-v3.2-think", "ernie-x1.1", "ernie-x1.1-preview",
		},
	},
	{
		Source: "https://docs.volcengine.com/docs/82379/1350667?lang=zh",
		Sunset: "2026-09-21",
		ModelIDs: []string{
			"kimi-k2-5-260127", "glm-4-7-251222", "doubao-seed-code-preview-251028",
			"doubao-seed-1-8-251228", "doubao-seed-1-6-vision-250815",
			"doubao-seed-1-6-250615", "doubao-seed-1-6-251015",
			"doubao-seed-1-6-flash-250828", "doubao-seed-1-6-flash-250615",
			"doubao-1-5-lite-32k-250115", "doubao-1-5-vision-pro-32k-250115",
			"doubao-1-5-pro-32k-250115", "doubao-1-5-pro-32k-character-250715",
			"doubao-seedance-1-5-pro-251215", "doubao-seedance-1.5-pro",
		},
	},
	{
		Source:   "https://ark.volcengine.com/docs/82379/2578673",
		Sunset:   "2026-08-18",
		ModelIDs: []string{"minimax-m2.7"},
	},
	{
		Source:   "https://ark.volcengine.com/docs/82379/2578673",
		Sunset:   "2026-08-08",
		ModelIDs: []string{"doubao-seed-2.0-code", "doubao-seed-2.0-pro"},
	},
	{
		Source:   "https://platform.claude.com/docs/en/about-claude/model-deprecations",
		Sunset:   "2026-08-05",
		ModelIDs: []string{"claude-opus-4-1", "claude-opus-4-1-20250805"},
	},
	{
		Source: "https://developers.openai.com/api/docs/deprecations",
		Sunset: "2026-09-24",
		ModelIDs: []string{
			"sora-2", "sora-2-pro", "sora-2-2025-10-06", "sora-2-2025-12-08", "sora-2-pro-2025-10-06",
		},
	},
	{
		Source:   "https://developers.openai.com/api/docs/deprecations",
		Sunset:   "2026-09-28",
		ModelIDs: []string{"gpt-3.5-turbo-instruct", "babbage-002", "davinci-002", "gpt-3.5-turbo-1106"},
	},
	{
		Source: "https://developers.openai.com/api/docs/deprecations",
		Sunset: "2026-10-23",
		ModelIDs: []string{
			"gpt-3.5-turbo", "gpt-3.5-turbo-0125", "gpt-3.5-turbo-completions",
			"gpt-4", "gpt-4-0613", "gpt-4-0613-completions", "gpt-4-completions",
			"gpt-4-1106-preview", "gpt-4-turbo", "gpt-4-turbo-2024-04-09", "gpt-4-turbo-completions",
			"gpt-4.1-nano", "gpt-4.1-nano-2025-04-14", "gpt-4o-2024-05-13", "gpt-image-1",
			"o1", "o1-2024-12-17", "o1-pro", "o1-pro-2025-03-19", "o3-mini", "o3-mini-2025-01-31",
			"o4-mini", "o4-mini-2025-04-16",
		},
	},
	{
		Source:   "https://developers.openai.com/api/docs/deprecations",
		Sunset:   "2026-12-01",
		ModelIDs: []string{"gpt-image-1-mini", "gpt-image-1.5", "chatgpt-image-latest"},
	},
	{
		Source: "https://developers.openai.com/api/docs/deprecations",
		Sunset: "2026-12-11",
		ModelIDs: []string{
			"gpt-5-2025-08-07", "gpt-5-mini-2025-08-07", "gpt-5-nano-2025-08-07",
			"gpt-5-pro-2025-10-06", "o3-2025-04-16", "o3-pro-2025-06-10",
		},
	},
	{
		Source:   "https://ai.google.dev/gemini-api/docs/deprecations",
		Sunset:   "2026-09-30",
		ModelIDs: []string{"gemini-omni-flash-preview"},
	},
	{
		Source:   "https://ai.google.dev/gemini-api/docs/deprecations",
		Sunset:   "2026-10-02",
		ModelIDs: []string{"gemini-2.5-flash-image"},
	},
	{
		Source:   "https://ai.google.dev/gemini-api/docs/deprecations",
		Sunset:   "2026-06-25",
		ModelIDs: []string{"gemini-3.1-flash-image-preview", "gemini-3-pro-image-preview"},
	},
	{
		Source:   "https://ai.google.dev/gemini-api/docs/deprecations",
		Sunset:   "2026-08-17",
		ModelIDs: []string{"imagen-4.0-generate-001", "imagen-4.0-fast-generate-001", "imagen-4.0-ultra-generate-001"},
	},
	{
		Source:   "https://ai.google.dev/gemini-api/docs/deprecations",
		Sunset:   "2026-06-30",
		ModelIDs: []string{"veo-2.0-generate-001", "veo-3.0-generate-001", "veo-3.0-fast-generate-001"},
	},
	{
		Source:   "https://ai.google.dev/gemini-api/docs/deprecations",
		Sunset:   "2026-06-01",
		ModelIDs: []string{"gemini-2.0-flash", "gemini-2.0-flash-001", "gemini-2.0-flash-lite", "gemini-2.0-flash-lite-001"},
	},
}

var catalogWithdrawnModelIDs = func() map[string]struct{} {
	out := make(map[string]struct{})
	for _, withdrawal := range catalogModelWithdrawals {
		for _, id := range withdrawal.ModelIDs {
			out[id] = struct{}{}
		}
	}
	return out
}()

// Shared by public pricing, account display presets and every user-menu source.
// Exact IDs only: retiring a snapshot must not hide a newer stable family alias.
func isCatalogModelRecommended(modelID string) bool {
	id := strings.TrimSpace(modelID)
	if _, tail, prefixed := strings.Cut(id, "/"); prefixed {
		id = tail
	}
	if _, deprecated := tkIsDeprecatedAnthropicModel(id); deprecated {
		return false
	}
	if _, deprecated := tkIsDeprecatedOpenAIModel(id); deprecated {
		return false
	}
	_, withdrawn := catalogWithdrawnModelIDs[id]
	return !withdrawn
}

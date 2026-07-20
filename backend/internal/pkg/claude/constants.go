// Package claude provides constants and helpers for Claude API integration.
package claude

import "strings"

// Claude Code 客户端相关常量

// Beta header 常量
//
// 这里的常量对齐真实 Claude Code CLI 的最新流量；具体 cc patch 见 CLICurrentVersion
// 与 deploy/aws/stage0/anthropic-http-mimicry-baselines.json 的 cc_version（单一真值源）。
// Anthropic 上游会基于 anthropic-beta 的完整集合判定请求来源；
// 缺少任何"官方 Claude Code 请求才会带"的 beta，都会被降级到第三方额度，
// 对应报错：`Third-party apps now draw from your extra usage, not your plan limits.`
const (
	BetaOAuth                    = "oauth-2025-04-20"
	BetaClaudeCode               = "claude-code-20250219"
	BetaInterleavedThinking      = "interleaved-thinking-2025-05-14"
	BetaFineGrainedToolStreaming = "fine-grained-tool-streaming-2025-05-14"
	BetaTokenCounting            = "token-counting-2024-11-01"
	BetaContext1M                = "context-1m-2025-08-07"
	BetaFastMode                 = "fast-mode-2026-02-01"

	BetaPromptCachingScope = "prompt-caching-scope-2026-01-05"
	BetaEffort             = "effort-2025-11-24"
	BetaRedactThinking     = "redact-thinking-2026-02-12"
	BetaContextManagement  = "context-management-2025-06-27"
	BetaExtendedCacheTTL   = "extended-cache-ttl-2025-04-11"

	// cc 2.1.152 抓包新增；fine-grained-tool-streaming 已从真实 CLI 流量中消失。
	BetaAdvisorTool     = "advisor-tool-2026-03-01"
	BetaAdvancedToolUse = "advanced-tool-use-2025-11-20"
	BetaCacheDiagnosis  = "cache-diagnosis-2026-04-07"

	// cc 2.1.154+ 抓包新增。
	BetaThinkingTokenCount = "thinking-token-count-2026-05-13"
	BetaStructuredOutputs  = "structured-outputs-2025-12-15"
)

// DroppedBetas 是转发时需要从 anthropic-beta header 中移除的 beta token 列表。
// 这些 token 是客户端特有的，不应透传给上游 API。
var DroppedBetas = []string{}

// DefaultBetaHeader Claude Code 客户端默认的 anthropic-beta header（Sonnet/Opus OAuth 回退）。
const DefaultBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," +
	BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," +
	BetaAdvisorTool + "," + BetaAdvancedToolUse + "," + BetaExtendedCacheTTL + "," + BetaCacheDiagnosis

// MessageBetaHeaderNoTools /v1/messages 在无工具时的 beta header
//
// NOTE: Claude Code OAuth credentials are scoped to Claude Code. When we "mimic"
// Claude Code for non-Claude-Code clients, we must include the claude-code beta
// even if the request doesn't use tools, otherwise upstream may reject the
// request as a non-Claude-Code API request.
const MessageBetaHeaderNoTools = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking

// MessageBetaHeaderWithTools /v1/messages 在有工具时的 beta header
const MessageBetaHeaderWithTools = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking

// CountTokensBetaHeader count_tokens 请求使用的 anthropic-beta header
const CountTokensBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," + BetaTokenCounting

// HaikuBetaHeader Haiku 模型 OAuth 回退 anthropic-beta（structured-outputs 变体）。
// 历史观察：2026-06 / cc 2.1.160 实测 haiku 存在服务端 A/B 灰度，structured-outputs
// 为多数态，故选它；分布详见 docs/spec-delta/cc-2.1.160.md。
const HaikuBetaHeader = BetaOAuth + "," + BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," +
	BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaAdvisorTool + "," +
	BetaStructuredOutputs + "," + BetaCacheDiagnosis

// APIKeyBetaHeader API-key 账号建议使用的 anthropic-beta header（不包含 oauth）
const APIKeyBetaHeader = BetaClaudeCode + "," + BetaInterleavedThinking + "," + BetaFineGrainedToolStreaming

// APIKeyHaikuBetaHeader Haiku 模型在 API-key 账号下使用的 anthropic-beta header（不包含 oauth / claude-code）
const APIKeyHaikuBetaHeader = BetaInterleavedThinking

// DefaultCacheControlTTL 是网关代理为自己生成的 cache_control 块默认使用的 ttl。
// 真实 Claude Code CLI 当前使用 "1h"，但本仓策略是"客户端透传 ttl 优先；
// 客户端缺省时统一使用 5m"，这样既不浪费 1h 缓存额度，也保留客户端自定义能力。
const DefaultCacheControlTTL = "5m"

// CLICurrentVersion 是 sub2api 当前对外伪装的 Claude Code CLI 版本号（三段 semver）。
// 用于 billing attribution block 中的 cc_version=X.Y.Z.{fp} 前缀以及 fingerprint 计算。
// 必须与 DefaultHeaders["User-Agent"] 中的版本号严格一致；不一致会被 Anthropic 判第三方。
const CLICurrentVersion = "2.1.215"

// JoinBetaHeader joins beta tokens into the wire anthropic-beta header value.
func JoinBetaHeader(betas []string) string {
	return strings.Join(betas, ",")
}

// FullClaudeCodeMimicryBetas 返回最"像"真实 Claude Code CLI 的完整 beta 列表（Sonnet/Opus），
// 用于 OAuth 账号伪装成 Claude Code 时使用。
// 顺序与近期 cc /v1/messages 抓包一致（patch 见 CLICurrentVersion / baselines.json）。
//
// 使用建议：
//   - OAuth 账号 + 非 haiku：追加这整份列表，再按需保留 client 带来的 beta。
//   - OAuth 账号 + haiku：使用 FullClaudeCodeHaikuMimicryBetas（无 effort / advanced-tool-use）。
//   - API-key 账号：不要使用本函数，参见 APIKeyBetaHeader。
//   - 不默认加入 redact-thinking，避免上游抹除 thinking 内容；客户端显式传入时由合并逻辑保留。
func FullClaudeCodeMimicryBetas() []string {
	return []string{
		BetaClaudeCode,
		BetaOAuth,
		BetaInterleavedThinking,
		BetaThinkingTokenCount,
		BetaContextManagement,
		BetaPromptCachingScope,
		BetaAdvisorTool,
		BetaAdvancedToolUse,
		BetaExtendedCacheTTL,
		BetaCacheDiagnosis,
	}
}

// FullClaudeCodeHaikuMimicryBetas 返回 Haiku 模型 OAuth mimicry 的 beta 列表（近期 cc structured-outputs 抓包）。
func FullClaudeCodeHaikuMimicryBetas() []string {
	return []string{
		BetaOAuth,
		BetaInterleavedThinking,
		BetaThinkingTokenCount,
		BetaContextManagement,
		BetaPromptCachingScope,
		BetaAdvisorTool,
		BetaStructuredOutputs,
		BetaCacheDiagnosis,
	}
}

// DefaultHeaders 是 Claude Code 客户端默认请求头（fingerprint 缺失时的兜底）。
//
// 单一真值来源纪律：OS / arch / runtime / runtime-version / UA 后缀 等"身份"字段
// 必须与 service 包的 canonical 抓包（canonicalHTTPObservedStatic +
// canonicalUASuffix，与唯一的 TLS ClientHello 同批抓取）逐字节一致。否则当
// fingerprint=nil 走兜底路径时，会发出 canonical TLS（Node24/MacOS 形态）+ Linux
// 头的自相矛盾指纹——比完全不伪装更容易被 Anthropic 判 third-party。
// 这一致性由 identity_canonical_consistency_test.go 机械锁死：任何一处漂移即测试失败。
// 包依赖方向为 service → claude，claude 无法反向 import service，故这里以同步字面量
// 承载，由守卫测试强制对齐。
var DefaultHeaders = map[string]string{
	"User-Agent":                                "claude-cli/2.1.215 (external, cli)",
	"X-Stainless-Lang":                          "js",
	"X-Stainless-Package-Version":               "0.94.0",
	"X-Stainless-OS":                            "MacOS",
	"X-Stainless-Arch":                          "arm64",
	"X-Stainless-Runtime":                       "node",
	"X-Stainless-Runtime-Version":               "v26.3.0",
	"X-Stainless-Retry-Count":                   "0",
	"X-Stainless-Timeout":                       "600",
	"X-App":                                     "cli",
	"Anthropic-Dangerous-Direct-Browser-Access": "true",
}

// Model 表示一个 Claude 模型
type Model struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// DefaultModels Claude Code 客户端支持的默认模型列表
var DefaultModels = []Model{
	{
		ID:          "claude-fable-5",
		Type:        "model",
		DisplayName: "Claude Fable 5",
		CreatedAt:   "2026-06-09T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-5-20251101",
		Type:        "model",
		DisplayName: "Claude Opus 4.5",
		CreatedAt:   "2025-11-01T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-1",
		Type:        "model",
		DisplayName: "Claude Opus 4.1",
		CreatedAt:   "2025-08-05T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-6",
		Type:        "model",
		DisplayName: "Claude Opus 4.6",
		CreatedAt:   "2026-02-06T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-7",
		Type:        "model",
		DisplayName: "Claude Opus 4.7",
		CreatedAt:   "2026-04-17T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-8",
		Type:        "model",
		DisplayName: "Claude Opus 4.8",
		CreatedAt:   "2026-05-29T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-5",
		Type:        "model",
		DisplayName: "Claude Sonnet 5",
		CreatedAt:   "2026-07-01T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-6",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.6",
		CreatedAt:   "2026-02-18T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-5-20250929",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.5",
		CreatedAt:   "2025-09-29T00:00:00Z",
	},
	{
		ID:          "claude-haiku-4-5-20251001",
		Type:        "model",
		DisplayName: "Claude Haiku 4.5",
		CreatedAt:   "2025-10-01T00:00:00Z",
	},
}

// DefaultModelIDs 返回默认模型的 ID 列表
func DefaultModelIDs() []string {
	ids := make([]string, len(DefaultModels))
	for i, m := range DefaultModels {
		ids[i] = m.ID
	}
	return ids
}

// DefaultTestModel 测试时使用的默认模型
const DefaultTestModel = "claude-sonnet-4-5-20250929"

// ModelIDOverrides Claude OAuth 请求需要的模型 ID 映射
var ModelIDOverrides = map[string]string{
	"claude-sonnet-4-5": "claude-sonnet-4-5-20250929",
	"claude-opus-4-5":   "claude-opus-4-5-20251101",
	"claude-haiku-4-5":  "claude-haiku-4-5-20251001",
}

// ModelIDReverseOverrides 用于将上游模型 ID 还原为短名
var ModelIDReverseOverrides = map[string]string{
	"claude-sonnet-4-5-20250929": "claude-sonnet-4-5",
	"claude-opus-4-5-20251101":   "claude-opus-4-5",
	"claude-haiku-4-5-20251001":  "claude-haiku-4-5",
}

// NormalizeModelID 根据 Claude OAuth 规则映射模型
func NormalizeModelID(id string) string {
	if id == "" {
		return id
	}
	if mapped, ok := ModelIDOverrides[id]; ok {
		return mapped
	}
	return id
}

// DenormalizeModelID 将上游模型 ID 转换为短名
func DenormalizeModelID(id string) string {
	if id == "" {
		return id
	}
	if mapped, ok := ModelIDReverseOverrides[id]; ok {
		return mapped
	}
	return id
}

// ModelsForIDs synthesizes a []Model for the given (servable) ids. The servable
// allowlist carries BASE ids (claude-opus-4-5) while DefaultModels carries the
// canonical (often DATED) form (claude-opus-4-5-20251101), so DefaultModels is
// indexed by its denormalized (base) id: a base servable id reuses the canonical
// entry (preserving the dated wire form + DisplayName), and allowlist-only ids
// absent from DefaultModels are synthesized. Shared by the gateway /v1/models
// fallback and the admin available-models surface so the two never drift on the
// synthesized display metadata.
func ModelsForIDs(ids []string) []Model {
	byBase := make(map[string]Model, len(DefaultModels))
	for _, m := range DefaultModels {
		byBase[DenormalizeModelID(m.ID)] = m
	}
	out := make([]Model, 0, len(ids))
	for _, id := range ids {
		if m, ok := byBase[id]; ok {
			out = append(out, m)
			continue
		}
		out = append(out, Model{
			ID:          id,
			Type:        "model",
			DisplayName: id,
			CreatedAt:   "2024-01-01T00:00:00Z",
		})
	}
	return out
}

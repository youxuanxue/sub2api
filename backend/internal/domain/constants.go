package domain

import "strings"

// Status constants
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusError    = "error"
	StatusUnused   = "unused"
	StatusUsed     = "used"
	StatusExpired  = "expired"
)

// Role constants
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// Platform constants
const (
	PlatformAnthropic   = "anthropic"
	PlatformOpenAI      = "openai"
	PlatformGemini      = "gemini"
	PlatformAntigravity = "antigravity"
	PlatformNewAPI      = "newapi"
	// PlatformKiro is the sixth platform: AWS Kiro / CodeWhisperer subscriptions
	// relayed via the vendored protocol layer in internal/integration/kiro.
	// Kiro speaks the CodeWhisperer EventStream protocol (not OpenAI-compatible),
	// so it is exposed through the native Anthropic /v1/messages path and schedules
	// in its own isolated pool — it is intentionally NOT an OpenAI-compat member.
	PlatformKiro = "kiro"
	// PlatformGrok is the seventh platform: xAI / Grok (SuperGrok Heavy) OAuth
	// subscriptions relayed to api.x.ai/v1. Unlike Kiro, xAI speaks the
	// OpenAI-compatible wire protocol, so grok is an OpenAI-compat pool member and
	// reuses the OpenAI-compat routing/scheduling/forward path — it differs from
	// the openai (Codex) platform only in its OAuth refresh endpoint and base URL.
	PlatformGrok = "grok"
)

// Account type constants
const (
	AccountTypeOAuth          = "oauth"           // OAuth类型账号（full scope: profile + inference）
	AccountTypeSetupToken     = "setup-token"     // Setup Token类型账号（inference only scope）
	AccountTypeAPIKey         = "apikey"          // API Key类型账号
	AccountTypeUpstream       = "upstream"        // 上游透传类型账号（通过 Base URL + API Key 连接上游）
	AccountTypeBedrock        = "bedrock"         // AWS Bedrock 类型账号（通过 SigV4 签名或 API Key 连接 Bedrock，由 credentials.auth_mode 区分）
	AccountTypeServiceAccount = "service_account" // Google Service Account 类型账号（用于 Vertex AI）
)

// Redeem type constants
const (
	RedeemTypeBalance          = "balance"
	RedeemTypeConcurrency      = "concurrency"
	RedeemTypeSubscription     = "subscription"
	RedeemTypeInvitation       = "invitation"
	RedeemTypeAffiliateBalance = "affiliate_balance"
)

// PromoCode status constants
const (
	PromoCodeStatusActive   = "active"
	PromoCodeStatusDisabled = "disabled"
)

// Admin adjustment type constants
const (
	AdjustmentTypeAdminBalance     = "admin_balance"     // 管理员调整余额
	AdjustmentTypeAdminConcurrency = "admin_concurrency" // 管理员调整并发数
)

// Group subscription type constants
const (
	SubscriptionTypeStandard     = "standard"     // 标准计费模式（按余额扣费）
	SubscriptionTypeSubscription = "subscription" // 订阅模式（按限额控制）
)

// Subscription status constants
const (
	SubscriptionStatusActive    = "active"
	SubscriptionStatusExpired   = "expired"
	SubscriptionStatusSuspended = "suspended"
	SubscriptionStatusRevoked   = "revoked"
)

// AntigravityGemini31ProAgentModel is the upstream route for Gemini 3.1 Pro High.
const AntigravityGemini31ProAgentModel = "gemini-pro-agent"

// DefaultAntigravityModelMapping 是 Antigravity 平台的默认模型映射
// 当账号未配置 model_mapping 时使用此默认值
// 与前端 useModelWhitelist.ts 中的 antigravityDefaultMappings 保持一致
var DefaultAntigravityModelMapping = map[string]string{
	// Claude 白名单（2026-07-07 live fetchAvailableModels: only these two are exposed
	// by cloudcode-pa for antigravity-oh1-ls-b; newer Claude ids return upstream 404).
	"claude-opus-4-6-thinking": "claude-opus-4-6-thinking",
	"claude-opus-4-6":          "claude-opus-4-6-thinking", // 简称映射
	"claude-sonnet-4-6":        "claude-sonnet-4-6",
	// Gemini 2.5 白名单
	"gemini-2.5-flash":      "gemini-2.5-flash",
	"gemini-2.5-flash-lite": "gemini-2.5-flash-lite",
	// 2.5-flash-image 上游对该账号返回 502（2026-06-15 prod 中继实测）→ 重指可服务的
	// 3.1-flash-image（保留别名兼容，客户端无需改名）。
	"gemini-2.5-flash-image":         "gemini-3.1-flash-image",
	"gemini-2.5-flash-image-preview": "gemini-3.1-flash-image",
	"gemini-2.5-flash-thinking":      "gemini-2.5-flash-thinking",
	"gemini-2.5-pro":                 "gemini-2.5-pro",
	// Gemini 3 白名单。gemini-3-pro-* 上游目录已无（仅剩 gemini-3-flash 与 gemini-3.1-pro-*）
	// → 2026-06-15 prod 中继实测 gemini-3-pro-high/low 返回 200 但 0/0（静默空响应）。
	// 重指到等价可服务 wire id：high→gemini-pro-agent（上游显示名同为 "Gemini 3.1 Pro (High)"）、
	// low→gemini-3.1-pro-low。
	"gemini-3-flash":    "gemini-3-flash",
	"gemini-3-pro-high": "gemini-pro-agent",
	"gemini-3-pro-low":  "gemini-3.1-pro-low",
	// Gemini 3 preview 映射
	"gemini-3-flash-preview": "gemini-3-flash",
	"gemini-3-pro-preview":   AntigravityGemini31ProAgentModel,
	// Gemini 3.1 白名单。gemini-3.1-pro-high 在上游 deprecatedModelIds 中，直接请求返回 400
	// （2026-06-15 实测）→ 重指 gemini-pro-agent（非弃用、同为 3.1 Pro High）。
	"gemini-3.1-pro":      AntigravityGemini31ProAgentModel,
	"gemini-3.1-pro-high": "gemini-pro-agent",
	"gemini-3.1-pro-low":  "gemini-3.1-pro-low",
	// Gemini 3.1 preview 映射
	"gemini-3.1-pro-preview": AntigravityGemini31ProAgentModel,
	// Gemini 3.1 image 白名单
	"gemini-3.1-flash-image": "gemini-3.1-flash-image",
	// Gemini 3.1 image preview 映射
	"gemini-3.1-flash-image-preview": "gemini-3.1-flash-image",
	// Gemini 3 image 兼容映射（向 3.1 image 迁移）
	"gemini-3-pro-image":         "gemini-3.1-flash-image",
	"gemini-3-pro-image-preview": "gemini-3.1-flash-image",
	// Gemini 3.5 Flash 实测 wire id（2026-06 /v1internal:fetchAvailableModels；
	// thinkingBudget 由 wire id 在上游决定，app 下拉显示名见各行注释）
	"gemini-3.5-flash-low":       "gemini-3.5-flash-low",       // app "Gemini 3.5 Flash (Medium)"
	"gemini-3.5-flash-extra-low": "gemini-3.5-flash-extra-low", // app "Gemini 3.5 Flash (Low)"
	"gemini-3-flash-agent":       "gemini-3-flash-agent",       // app "Gemini 3.5 Flash (High)"
	"gemini-3.5-flash":           "gemini-3.5-flash-low",       // 友好别名 → Medium 档
	// Gemini 3.1 Pro (High) 实测 wire id（gemini-3.1-pro-high 上游已废弃 → gemini-pro-agent）
	"gemini-pro-agent": "gemini-pro-agent",
	// 其他官方模型
	"gpt-oss-120b-medium": "gpt-oss-120b-medium",
}

var antigravityStructuralDeadModelMappingKeys = map[string]struct{}{
	// PR #921 inventory + 2026-06-22 live snapshot: these are stale request aliases
	// in persisted antigravity account mappings. Keep DefaultAntigravityModelMapping
	// compatibility remaps, but do not write these aliases into canonical accounts.
	"gemini-2.5-flash-image-preview": {},
	"gemini-3-flash-preview":         {},
	"gemini-3-pro-high":              {},
	"gemini-3-pro-image-preview":     {},
	"gemini-3-pro-low":               {},
	"gemini-3-pro-preview":           {},
	"gemini-3.1-pro-high":            {},
	"gemini-3.1-pro-preview":         {},
}

var antigravityUnpricedModelMappingKeys = map[string]struct{}{
	// Served by Antigravity but absent from reliable public pricing. Keeping it
	// in a visible/custom account mapping bills successful requests at $0.
	"tab_flash_lite_preview": {},
}

// IsAntigravityStructuralDeadModelMappingKey reports whether k is a stale
// Antigravity request alias that should not be persisted in canonical account
// mappings. DefaultAntigravityModelMapping may still keep compatibility remaps.
func IsAntigravityStructuralDeadModelMappingKey(k string) bool {
	_, ok := antigravityStructuralDeadModelMappingKeys[k]
	return ok
}

// IsAntigravityUnpricedModelMappingKey reports whether k is an Antigravity model
// mapping key that must not be persisted because no reliable public price exists.
func IsAntigravityUnpricedModelMappingKey(k string) bool {
	_, ok := antigravityUnpricedModelMappingKeys[k]
	return ok
}

// GeminiOnlyAntigravityModelMapping 是 DefaultAntigravityModelMapping 去掉所有
// claude-*、gpt-oss-* 与 #921 confirmed structural-dead 兼容别名后的
// 「gemini-only」服务映射——运营策略下 antigravity 只服务 gemini（claude 路由到
// anthropic、gpt-oss 移出 antigravity）的规范账号映射，由
// AntigravityConfigReconciler 自动写入每个 antigravity 账号。
//
// 在 DefaultAntigravityModelMapping 上方新增一个 gemini wire id 会自动流入此处（单一
// 真值源），除非它是 structural-dead 兼容别名。缺少可靠公开价的模型不应进入这里；
// 否则它会作为可调模型服务并按 $0 记账。
var GeminiOnlyAntigravityModelMapping = buildGeminiOnlyAntigravityModelMapping()

func buildGeminiOnlyAntigravityModelMapping() map[string]string {
	out := make(map[string]string, len(DefaultAntigravityModelMapping))
	for k, v := range DefaultAntigravityModelMapping {
		if strings.HasPrefix(k, "claude-") || strings.HasPrefix(k, "gpt-oss-") {
			continue
		}
		if IsAntigravityStructuralDeadModelMappingKey(k) {
			continue
		}
		if IsAntigravityUnpricedModelMappingKey(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// GeminiOnlyAntigravityModelScopes 是 gemini-only 运营策略下 antigravity 分组的规范
// supported_model_scopes：只 gemini 文本 + gemini 图片，不含 claude（claude 路由到
// anthropic）。AntigravityConfigReconciler 把每个 antigravity 分组的 scopes 自愈为此值，
// 与「每个 antigravity 账号 model_mapping 收成 GeminiOnlyAntigravityModelMapping」对称——
// 使 /antigravity/v1/models 与 API key 使用指南对新建/漂移分组都自动隐藏 claude。
// scope 词表为 claude / gemini_text / gemini_image（migration 046b），故 gemini-only 即此两项。
var GeminiOnlyAntigravityModelScopes = []string{"gemini_text", "gemini_image"}

// DefaultBedrockModelMapping 是 AWS Bedrock 平台的默认模型映射
// 将 Anthropic 标准模型名映射到 Bedrock 模型 ID
// 注意：此处的 "us." 前缀仅为默认值，ResolveBedrockModelID 会根据账号配置的
// aws_region 自动调整为匹配的区域前缀（如 eu.、apac.、jp. 等）
var DefaultBedrockModelMapping = map[string]string{
	// Claude Fable
	"claude-fable-5": "anthropic.claude-fable-5",
	// Claude Opus
	"claude-opus-4-8":          "us.anthropic.claude-opus-4-8-v1",
	"claude-opus-4-7":          "us.anthropic.claude-opus-4-7-v1",
	"claude-opus-4-6-thinking": "us.anthropic.claude-opus-4-6-v1",
	"claude-opus-4-6":          "us.anthropic.claude-opus-4-6-v1",
	"claude-opus-4-5-thinking": "us.anthropic.claude-opus-4-5-20251101-v1:0",
	"claude-opus-4-5-20251101": "us.anthropic.claude-opus-4-5-20251101-v1:0",
	"claude-opus-4-1":          "us.anthropic.claude-opus-4-1-20250805-v1:0",
	"claude-opus-4-20250514":   "us.anthropic.claude-opus-4-20250514-v1:0",
	// Claude Sonnet
	"claude-sonnet-5":            "us.anthropic.claude-sonnet-5-v1",
	"claude-sonnet-4-6-thinking": "us.anthropic.claude-sonnet-4-6",
	"claude-sonnet-4-6":          "us.anthropic.claude-sonnet-4-6",
	"claude-sonnet-4-5":          "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
	"claude-sonnet-4-5-thinking": "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
	"claude-sonnet-4-5-20250929": "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
	"claude-sonnet-4-20250514":   "us.anthropic.claude-sonnet-4-20250514-v1:0",
	// Claude Haiku
	"claude-haiku-4-5":          "us.anthropic.claude-haiku-4-5-20251001-v1:0",
	"claude-haiku-4-5-20251001": "us.anthropic.claude-haiku-4-5-20251001-v1:0",
}

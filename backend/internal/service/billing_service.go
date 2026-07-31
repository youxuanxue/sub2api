package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// APIKeyRateLimitCacheData holds rate limit usage data cached in Redis.
type APIKeyRateLimitCacheData struct {
	Usage5h  float64 `json:"usage_5h"`
	Usage1d  float64 `json:"usage_1d"`
	Usage7d  float64 `json:"usage_7d"`
	Window5h int64   `json:"window_5h"` // unix timestamp, 0 = not started
	Window1d int64   `json:"window_1d"`
	Window7d int64   `json:"window_7d"`
}

// UserPlatformQuotaKey 标识一个 user×platform，用于脏集出入与批量读。
type UserPlatformQuotaKey struct {
	UserID   int64
	Platform string
}

// UserPlatformQuotaCacheEntry Redis hash 反序列化结果。
//
// SchemaVersion 用于向后兼容：
//   - 0（旧 entry，无 SchemaVersion 字段）→ 视为 cache MISS，强制 refresh
//   - 1（当前版本）→ 包含 limits 和 window_start，可免 DB 查询
//
// limit 字段为 nil 表示"无限额"（DB 中对应列为 NULL）。
const UserPlatformQuotaCacheSchemaV1 = int64(1)

type UserPlatformQuotaCacheEntry struct {
	DailyUsageUSD   float64
	WeeklyUsageUSD  float64
	MonthlyUsageUSD float64
	Version         int64
	SchemaVersion   int64

	// 以下字段仅在 SchemaVersion >= 1 时有效
	DailyLimitUSD   *float64
	WeeklyLimitUSD  *float64
	MonthlyLimitUSD *float64

	DailyWindowStart   *time.Time
	WeeklyWindowStart  *time.Time
	MonthlyWindowStart *time.Time
}

// BillingCache defines cache operations for billing service
type BillingCache interface {
	// Balance operations
	GetUserBalance(ctx context.Context, userID int64) (float64, error)
	SetUserBalance(ctx context.Context, userID int64, balance float64) error
	DeductUserBalance(ctx context.Context, userID int64, amount float64) error
	InvalidateUserBalance(ctx context.Context, userID int64) error

	// Subscription operations
	GetSubscriptionCache(ctx context.Context, userID, groupID int64) (*SubscriptionCacheData, error)
	SetSubscriptionCache(ctx context.Context, userID, groupID int64, data *SubscriptionCacheData) error
	UpdateSubscriptionUsage(ctx context.Context, userID, groupID int64, cost float64) error
	InvalidateSubscriptionCache(ctx context.Context, userID, groupID int64) error

	// API Key rate limit operations
	GetAPIKeyRateLimit(ctx context.Context, keyID int64) (*APIKeyRateLimitCacheData, error)
	SetAPIKeyRateLimit(ctx context.Context, keyID int64, data *APIKeyRateLimitCacheData) error
	UpdateAPIKeyRateLimitUsage(ctx context.Context, keyID int64, cost float64) error
	InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error

	// user × platform quota 缓存
	GetUserPlatformQuotaCache(ctx context.Context, userID int64, platform string) (*UserPlatformQuotaCacheEntry, bool, error)
	SetUserPlatformQuotaCache(ctx context.Context, userID int64, platform string, entry *UserPlatformQuotaCacheEntry, ttl time.Duration) error
	DeleteUserPlatformQuotaCache(ctx context.Context, userID int64, platform string) error
	// IncrUserPlatformQuotaUsageCache 在缓存命中时累加用量；缓存未命中（key 不存在）静默返回 nil。
	// markDirty=true 时将该 key 的 member 写入 Redis 脏集，供 flusher 批量回写 DB。
	IncrUserPlatformQuotaUsageCache(ctx context.Context, userID int64, platform string, cost float64, ttl time.Duration, markDirty bool) error

	// 脏集读写，供 flusher 使用。
	PopDirtyUserPlatformQuotaKeys(ctx context.Context, n int) ([]UserPlatformQuotaKey, error)
	ReaddDirtyUserPlatformQuotaKeys(ctx context.Context, keys []UserPlatformQuotaKey) error
	BatchGetUserPlatformQuotaCache(ctx context.Context, keys []UserPlatformQuotaKey) ([]*UserPlatformQuotaCacheEntry, error)
}

// ModelPricing 模型价格配置（per-token价格，与LiteLLM格式一致）
type ModelPricing struct {
	InputPricePerToken                 float64           // 每token输入价格 (USD)
	InputPricePerTokenPriority         float64           // priority service tier 下每token输入价格 (USD)
	ImageInputPricePerToken            float64           // 图片输入 token 价格 (USD)，用于多模态 embedding 等图文不同价场景；为 0 时回退到 InputPricePerToken
	OutputPricePerToken                float64           // 每token输出价格 (USD)
	OutputPricePerTokenPriority        float64           // priority service tier 下每token输出价格 (USD)
	ThinkingOutputPricePerToken        float64           // 思考模式下每token输出价格 (USD)；0 = 该模型无思考溢价。见 computeTokenBreakdown
	CacheCreationPricePerToken         float64           // 缓存创建每token价格 (USD)
	CacheCreationPricePerTokenPriority float64           // priority service tier 下缓存创建每token价格 (USD)
	CacheCreationPriceExplicit         bool              // 是否由渠道/区间定价显式设定（为 true 时即使 == 0 也不回退）
	CacheReadPricePerToken             float64           // 缓存读取每token价格 (USD)
	CacheReadPricePerTokenPriority     float64           // priority service tier 下缓存读取每token价格 (USD)
	CacheCreation5mPrice               float64           // 5分钟缓存创建每token价格 (USD)
	CacheCreation1hPrice               float64           // 1小时缓存创建每token价格 (USD)
	SupportsCacheBreakdown             bool              // 是否支持详细的缓存分类
	LongContextInputThreshold          int               // 超过阈值后按整次会话提升输入价格
	LongContextInputMultiplier         float64           // 长上下文整次会话输入倍率
	LongContextOutputMultiplier        float64           // 长上下文整次会话输出倍率
	ImageOutputPricePerToken           float64           // 图片输出 token 价格 (USD)
	ImageOutputPriceExplicit           bool              // 是否由渠道定价显式设定（为 true 时即使 == 0 也不回退）
	Intervals                          []PricingInterval // 输入-token 区间分档（来自 TK overlay；空 = 扁平）。接进 ResolvedPricing.Intervals。
}

func normalizeBillingServiceTier(serviceTier string) string {
	return strings.ToLower(strings.TrimSpace(serviceTier))
}

func usePriorityServiceTierPricing(serviceTier string, pricing *ModelPricing) bool {
	if pricing == nil || normalizeBillingServiceTier(serviceTier) != "priority" {
		return false
	}
	return pricing.InputPricePerTokenPriority > 0 || pricing.OutputPricePerTokenPriority > 0 ||
		pricing.CacheCreationPricePerTokenPriority > 0 || pricing.CacheReadPricePerTokenPriority > 0
}

func serviceTierCostMultiplier(serviceTier string) float64 {
	switch normalizeBillingServiceTier(serviceTier) {
	case "priority":
		return 2.0
	case "flex":
		return 0.5
	default:
		return 1.0
	}
}

// UsageTokens 使用的token数量
type UsageTokens struct {
	InputTokens           int
	ImageInputTokens      int
	OutputTokens          int
	CacheCreationTokens   int
	CacheReadTokens       int
	CacheCreation5mTokens int
	CacheCreation1hTokens int
	ImageOutputTokens     int
}

// CostBreakdown 费用明细
type CostBreakdown struct {
	InputCost                 float64
	OutputCost                float64
	ImageOutputCost           float64
	CacheCreationCost         float64
	CacheReadCost             float64
	TotalCost                 float64
	ActualCost                float64 // 应用倍率后的实际费用
	BillingMode               string  // 计费模式（"token"/"per_request"/"image"），由 CalculateCostUnified 填充
	LongContextBillingApplied bool
}

// ErrModelPricingUnavailable indicates that the registry snapshot has no
// resolved owner for the requested model (and no scoped channel override is
// available at the caller's resolution layer).
var ErrModelPricingUnavailable = errors.New("pricing not found")

// BillingService 计费服务
type BillingService struct {
	cfg            *config.Config
	pricingService *PricingService

	// registryAliasWarnSeen records models that already emitted the registry-alias
	// convergence warning (normalized to lowercase),
	// so each model emits at most one "Using registry alias pricing" message per process,
	// 避免热路径上每请求刷屏(issue #3394)。零值即可用,无需在构造函数初始化。
	registryAliasWarnSeen sync.Map
}

// NewBillingService 创建计费服务实例
func NewBillingService(cfg *config.Config, pricingService *PricingService) *BillingService {
	s := &BillingService{
		cfg:            cfg,
		pricingService: pricingService,
	}
	return s
}

func (s *BillingService) registryOwnerPricing(modelKey string) *ModelPricing {
	return tkOverlayModelPricing(modelKey)
}

// getRegistryAliasPricing resolves a compatibility/family alias to another
// registry owner row. It never contains pricing numbers; all dimensions come
// from the resolved registry snapshot.
func (s *BillingService) getRegistryAliasPricing(model string) *ModelPricing {
	modelLower := strings.ToLower(model)

	// 按模型系列匹配
	if strings.Contains(modelLower, "opus") {
		// "opus-5" 必须先判：不能用裸 "5" 匹配，否则 claude-opus-4-5 会被误判。
		if strings.Contains(modelLower, "opus-5") || strings.Contains(modelLower, "opus5") {
			return s.registryOwnerPricing("claude-opus-5")
		}
		if strings.Contains(modelLower, "4.8") || strings.Contains(modelLower, "4-8") {
			return s.registryOwnerPricing("claude-opus-4.8")
		}
		if strings.Contains(modelLower, "4.7") || strings.Contains(modelLower, "4-7") {
			return s.registryOwnerPricing("claude-opus-4.7")
		}
		if strings.Contains(modelLower, "4.6") || strings.Contains(modelLower, "4-6") {
			return s.registryOwnerPricing("claude-opus-4.6")
		}
		if strings.Contains(modelLower, "4.5") || strings.Contains(modelLower, "4-5") {
			return s.registryOwnerPricing("claude-opus-4.5")
		}
		return s.registryOwnerPricing("claude-3-opus")
	}
	if strings.Contains(modelLower, "sonnet") {
		if strings.Contains(modelLower, "4") && !strings.Contains(modelLower, "3") {
			return s.registryOwnerPricing("claude-sonnet-4")
		}
		return s.registryOwnerPricing("claude-3-5-sonnet")
	}
	if strings.Contains(modelLower, "haiku") {
		if strings.Contains(modelLower, "3-5") || strings.Contains(modelLower, "3.5") {
			return s.registryOwnerPricing("claude-3-5-haiku")
		}
		return s.registryOwnerPricing("claude-3-haiku")
	}
	// Claude Fable 5（Opus 之上的新档）必须先于下面的 claude 兜底命中，
	// 否则会被误判为 sonnet（$3/$15）严重少收费。
	if strings.Contains(modelLower, "fable") {
		return s.registryOwnerPricing("claude-fable-5")
	}
	// Claude unknown variants resolve to the registry's Sonnet family owner.
	if strings.Contains(modelLower, "claude") {
		return s.registryOwnerPricing("claude-sonnet-4")
	}
	if strings.Contains(modelLower, "gemini-3.1-pro") || strings.Contains(modelLower, "gemini-3-1-pro") {
		return s.registryOwnerPricing("gemini-3.1-pro")
	}
	if strings.Contains(modelLower, "gemini-3.6-flash") || strings.Contains(modelLower, "gemini-3-6-flash") {
		return s.registryOwnerPricing("gemini-3.6-flash")
	}
	// Gemini family-grained registry alias (docs/approved/priced-or-it-doesnt-ship.md pivot). A gemini id with
	// no direct registry owner row resolves to its FAMILY median floor (never $0, never rejected); the
	// served_at_fallback alert then drives a real-price fill. "pro" → pro floor; flash / flash-lite /
	// unknown gemini → flash-tier median. (Family granularity + median tier bound the mischarge that a
	// single flat rate would cause — that was the C-era objection, addressed here, not avoided.)
	// TK: See upstream Wei-Shaw/sub2api#2486 — gemini-pro-agent and other unknown gemini-* ids must
	// never return nil here; nil caused calculateTokenCost to record ActualCost=0 with no quota deduction.
	if strings.Contains(modelLower, "gemini") {
		if strings.Contains(modelLower, "pro") {
			return s.registryOwnerPricing("gemini-2.5-pro")
		}
		if strings.Contains(modelLower, "flash-lite") || strings.Contains(modelLower, "flash-8b") {
			return s.registryOwnerPricing("gemini-2.5-flash-lite")
		}
		return s.registryOwnerPricing("gemini-2.5-flash")
	}

	// DeepSeek V4 系列：仅匹配已知 V4 Pro/Flash 与官方兼容别名
	// （deepseek-chat / deepseek-reasoner → V4 Flash），未知 deepseek-* 型号不做 alias，避免误计价。
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
	// 与 DeepSeek 一样采用"白名单"语义：未在本表命中的国产模型不解析 registry alias，避免误计价。

	// 智谱 GLM：定价源只用 BigModel 官方 pricing 页；可服务路径仍是 Qwen/DashScope 池。
	// 匹配顺序：先判别最高 tier，再依次降级。
	if canonical := normalizeGLMVolcengineDatedModelID(modelLower); canonical != "" {
		if pricing := tkOverlayModelPricing(canonical); pricing != nil {
			return pricing
		}
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
		return s.registryOwnerPricing("glm-4.7-flash")
	}
	if strings.Contains(modelLower, "glm-4.7") {
		return tkOverlayModelPricing("glm-4.7")
	}
	if strings.Contains(modelLower, "glm-4.6") {
		return tkOverlayModelPricing("glm-4.6")
	}
	if strings.Contains(modelLower, "glm-4.5-flash") {
		return s.registryOwnerPricing("glm-4.5-flash")
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

	// 月之暗面 Kimi（kimi-k3 / k3 / k3-256k / kimi-k2.6 / kimi-for-coding / kimi-k2.5 / kimi-k2-thinking / kimi-k2）
	// K2-0905 / K2-0711 官方未保留定价，不进入 fallback。
	// K3 规则置于 K2 前：API Platform 仅官方 kimi-k3（及 / 路径后缀）；
	// Code bare aliases 仅精确 k3 / k3-256k 或 /k3|/k3-256k 后缀，避免 kimi-k30 等未知型号误命中。
	// 注意：kimi-k3[1m] 是 Claude Code 上下文选择语法，不是 Kimi API 模型 ID，不进入 fallback。
	if strings.Contains(modelLower, "kimi-for-coding") {
		return tkOverlayModelPricing("kimi-k2.6")
	}
	if modelLower == "kimi-k3" || strings.HasSuffix(modelLower, "/kimi-k3") ||
		modelLower == "k3" || modelLower == "k3-256k" ||
		strings.HasSuffix(modelLower, "/k3") || strings.HasSuffix(modelLower, "/k3-256k") {
		return s.registryOwnerPricing("kimi-k3")
	}
	if strings.Contains(modelLower, "kimi-k2.6") || strings.Contains(modelLower, "kimi-k2-6") {
		return tkOverlayModelPricing("kimi-k2.6")
	}
	if strings.Contains(modelLower, "kimi-k2.5") || strings.Contains(modelLower, "kimi-k2-5") {
		return tkOverlayModelPricing("kimi-k2.5")
	}
	if strings.Contains(modelLower, "kimi-k2-thinking") || strings.Contains(modelLower, "kimi-k2-thinking-") {
		return s.registryOwnerPricing("kimi-k2-thinking")
	}
	if strings.Contains(modelLower, "kimi-k2") || strings.Contains(modelLower, "kimi/k2") {
		return s.registryOwnerPricing("kimi-k2")
	}

	// MiniMax M 系列（M3 / M2.7 / M2.5 / M2.1 / M2；含 highspeed 变体）
	if strings.Contains(modelLower, "minimax-m3") {
		return s.registryOwnerPricing("minimax-m3")
	}
	if strings.Contains(modelLower, "minimax-m2.7-highspeed") || strings.Contains(modelLower, "minimax-m2-7-highspeed") {
		return s.registryOwnerPricing("minimax-m2.7-highspeed")
	}
	if strings.Contains(modelLower, "minimax-m2.7") || strings.Contains(modelLower, "minimax-m2-7") {
		return s.registryOwnerPricing("minimax-m2.7")
	}
	if strings.Contains(modelLower, "minimax-m2.5") || strings.Contains(modelLower, "minimax-m2-5") {
		return s.registryOwnerPricing("minimax-m2.5")
	}
	if strings.Contains(modelLower, "minimax-m2.1") || strings.Contains(modelLower, "minimax-m2-1") {
		return s.registryOwnerPricing("minimax-m2.1")
	}
	if strings.Contains(modelLower, "minimax-m2") || strings.Contains(modelLower, "minimax-m-2") {
		return s.registryOwnerPricing("minimax-m2")
	}

	// 火山方舟 豆包 Embedding（多模态向量化）。
	// most-specific-first：放在未来任何 doubao-embedding / doubao 宽匹配之前。
	// 覆盖带版本后缀的别名（如 doubao-embedding-vision-251215）。
	if strings.Contains(modelLower, "doubao-embedding-vision") {
		return s.registryOwnerPricing("doubao-embedding-vision")
	}

	// OpenAI（GPT-5 / Codex 族）：仅匹配已知型号，避免未知 OpenAI 型号误计价。
	if normalized := normalizeOpenAIBillingModel(modelLower); normalized != "" {
		switch normalized {
		case "gpt-5.6-sol":
			return tkOverlayModelPricing("gpt-5.6-sol")
		case "gpt-5.6-terra":
			return tkOverlayModelPricing("gpt-5.6-terra")
		case "gpt-5.6-luna":
			return tkOverlayModelPricing("gpt-5.6-luna")
		case "gpt-5.5-pro":
			return s.registryOwnerPricing("gpt-5.5-pro")
		case "gpt-5.5":
			return s.registryOwnerPricing("gpt-5.5")
		case "gpt-5.4-mini":
			return s.registryOwnerPricing("gpt-5.4-mini")
		case "gpt-5.4-nano":
			return s.registryOwnerPricing("gpt-5.4-nano")
		case "gpt-5.4":
			return s.registryOwnerPricing("gpt-5.4")
		case "gpt-5.2":
			return s.registryOwnerPricing("gpt-5.2")
		case "gpt-5.3-codex", "gpt-5.3-codex-spark":
			return s.registryOwnerPricing("gpt-5.3-codex")
		}
	}
	// GPT 家族中位 floor（docs/approved/priced-or-it-doesnt-ship.md pivot）：已知型号上面已返具体价；
	// 其余【chat 形】gpt-*（新变体 / 未登记 codex 后缀）→ gpt-5.4-tier 中位 floor，而非「未知即拒」，
	// 配 served_at_fallback 告警 + 快补真价（解 §9 R-openai 别名）。诚实标注（对抗复审 S2）：gpt 主线
	// 中位 ≈ gpt-5.4，但 premium 未知（如未镜像的 gpt-5.5，real out 3e-5）会被【欠收 ~2×】——欠收是临时
	// 小漏、由告警驱动几分钟内补真价。**非 chat 模式的 gpt（image/audio/realtime/transcribe/tts）排除在
	// floor 外** → 它们走 registry 价或 reject，绝不按 token 中位误计成错模式。
	// 真正异类的 OpenAI 模型（o 系列等）不含 "gpt" → 同样走真价或 reject。
	if strings.Contains(modelLower, "gpt") &&
		!strings.Contains(modelLower, "image") && !strings.Contains(modelLower, "audio") &&
		!strings.Contains(modelLower, "realtime") && !strings.Contains(modelLower, "transcribe") &&
		!strings.Contains(modelLower, "-tts") {
		return s.registryOwnerPricing("gpt-5.4")
	}

	switch modelLower {
	case "grok", "grok-latest", "grok-4.5", "grok-4.5-latest", "grok-build-latest":
		return s.registryOwnerPricing("grok-4.5")
	case "grok-4.3",
		"grok-4.20-0309-reasoning",
		"grok-4.20-0309-non-reasoning",
		"grok-4.20-multi-agent-0309",
		"grok-4.20-reasoning",
		"grok-4.20-non-reasoning":
		return s.registryOwnerPricing("grok-4.3")
	case "grok-build", "grok-build-0.1", "grok-composer", "grok-composer-2.5-fast", "composer-2.5":
		return s.registryOwnerPricing("grok-build-0.1")
	}

	return nil
}

func tkModelPricingFromLiteLLM(p *LiteLLMModelPricing) *ModelPricing {
	if p == nil || (p.TokenPricingAbsent && !p.ExplicitFree) || (tkIsEffectivelyUnpriced(p) && !p.ExplicitFree) {
		return nil
	}
	price5m := p.CacheCreationInputTokenCost
	price1h := p.CacheCreationInputTokenCostAbove1hr
	return &ModelPricing{
		InputPricePerToken:                 p.InputCostPerToken,
		InputPricePerTokenPriority:         p.InputCostPerTokenPriority,
		OutputPricePerToken:                p.OutputCostPerToken,
		OutputPricePerTokenPriority:        p.OutputCostPerTokenPriority,
		ThinkingOutputPricePerToken:        p.ThinkingOutputCostPerToken,
		CacheCreationPricePerToken:         p.CacheCreationInputTokenCost,
		CacheCreationPricePerTokenPriority: p.CacheCreationInputTokenCostPriority,
		CacheReadPricePerToken:             p.CacheReadInputTokenCost,
		CacheReadPricePerTokenPriority:     p.CacheReadInputTokenCostPriority,
		CacheCreation5mPrice:               price5m,
		CacheCreation1hPrice:               price1h,
		SupportsCacheBreakdown:             price1h > 0 && price1h > price5m,
		LongContextInputThreshold:          p.LongContextInputTokenThreshold,
		LongContextInputMultiplier:         p.LongContextInputCostMultiplier,
		LongContextOutputMultiplier:        p.LongContextOutputCostMultiplier,
		ImageOutputPricePerToken:           p.OutputCostPerImageToken,
		ImageInputPricePerToken:            p.InputCostPerImageToken,
		Intervals:                          p.Intervals,
	}
}

func tkOverlayModelPricing(model string) *ModelPricing {
	return tkModelPricingFromLiteLLM(loadTKPricingOverlay()[strings.ToLower(strings.TrimSpace(model))])
}

// IsServedViaFamilyFloor reports whether `model` bills through a registry family
// alias rather than a direct owner row. This is an estimated registry floor and
// the convergence signal for the post-pivot design: served_at_fallback alert →
// operator fills the model's direct owner row. Channel pricing is NOT consulted
// here (callers that bill via channel skip this); a model with a direct owner, or
// with no registry alias at all (→ gate-rejected), returns false.
func (s *BillingService) IsServedViaFamilyFloor(model string) bool {
	if s == nil || s.pricingService == nil {
		return false
	}
	lower := strings.ToLower(model)
	if real := s.pricingService.GetModelPricing(lower); real != nil && !tkIsEffectivelyUnpriced(real) {
		return false // has a real registry price → not a floor
	}
	return s.getRegistryAliasPricing(lower) != nil // no direct owner, but a registry family alias exists
}

// GetModelPricing 获取模型价格配置
func (s *BillingService) GetModelPricing(model string) (*ModelPricing, error) {
	// 标准化模型名称（转小写）
	model = strings.ToLower(model)

	// 1. 优先从 registry snapshot 获取直接 owner row
	// TK: 全零价条目视同未命中（registry 用 0.0 表示"价格未知"而非"免费"），
	// 落入 registry alias lookup / ErrModelPricingUnavailable，让缺价 funnel 记零成本并告警。
	if s.pricingService != nil {
		registryPricing := s.pricingService.GetModelPricing(model)
		// 仅有图片价、无 token 价的 registry 条目（如 imagen 类模型）不能用于
		// token 计费：直接返回会把 token 流量按 $0 计费。跳过后走 registry alias，
		// 无 alias 则 fail-closed（ErrModelPricingUnavailable）。
		// 图片计费路径（getDefaultImagePrice / getImageUnitPrice）直接读
		// PricingService，不受影响。
		if registryPricing != nil && registryPricing.TokenPricingAbsent {
			registryPricing = nil
		}
		if registryPricing != nil && !tkIsEffectivelyUnpriced(registryPricing) {
			return s.applyModelSpecificPricingPolicy(model, tkModelPricingFromLiteLLM(registryPricing)), nil
		}
	}

	// 2. Resolve a compatibility/family alias. The Go policy stores only
	// alias → registry-owner relationships; it never duplicates price numbers.
	registryAliasPricing := s.getRegistryAliasPricing(model)
	if registryAliasPricing != nil {
		// 按模型名去重:每个模型每进程最多打一条 warn,避免热路径每请求刷屏（issue #3394）。
		// model 在函数入口已 ToLower,故 GLM-5.2 / glm-5.2 视为同一条目。
		if _, seen := s.registryAliasWarnSeen.LoadOrStore(model, struct{}{}); !seen {
			log.Printf("[Billing] Using registry alias pricing for model: %s", model)
		}
		return s.applyModelSpecificPricingPolicy(model, tkApplyOfficialListBaseTaxForModel(model, registryAliasPricing)), nil
	}

	return nil, fmt.Errorf("%w for model: %s", ErrModelPricingUnavailable, model)
}

// GetModelPricingWithChannel 获取模型定价，渠道 scoped override 覆盖 registry owner。
// 渠道存在时，未配置的图片输出价格归零（不继承全局 owner）。
func (s *BillingService) GetModelPricingWithChannel(model string, channelPricing *ChannelModelPricing) (*ModelPricing, error) {
	pricing, err := s.GetModelPricing(model)
	if err != nil {
		return nil, err
	}
	if channelPricing == nil {
		return pricing, nil
	}
	// 防止修改 registry 返回的共享指针
	cloned := *pricing
	pricing = &cloned
	if channelPricing.InputPrice != nil {
		pricing.InputPricePerToken = *channelPricing.InputPrice
		pricing.InputPricePerTokenPriority = *channelPricing.InputPrice
	}
	if channelPricing.OutputPrice != nil {
		pricing.OutputPricePerToken = *channelPricing.OutputPrice
		pricing.OutputPricePerTokenPriority = *channelPricing.OutputPrice
	}
	if channelPricing.CacheWritePrice != nil {
		pricing.CacheCreationPricePerToken = *channelPricing.CacheWritePrice
		pricing.CacheCreationPricePerTokenPriority = *channelPricing.CacheWritePrice
		pricing.CacheCreationPriceExplicit = true
		pricing.CacheCreation5mPrice = *channelPricing.CacheWritePrice
		pricing.CacheCreation1hPrice = *channelPricing.CacheWritePrice
	}
	if channelPricing.CacheReadPrice != nil {
		pricing.CacheReadPricePerToken = *channelPricing.CacheReadPrice
		pricing.CacheReadPricePerTokenPriority = *channelPricing.CacheReadPrice
	}
	if channelPricing.ImageOutputPrice != nil {
		pricing.ImageOutputPricePerToken = *channelPricing.ImageOutputPrice
	} else {
		pricing.ImageOutputPricePerToken = 0
	}
	pricing.ImageOutputPriceExplicit = true
	return pricing, nil
}

// --- 统一计费入口 ---

// CostInput 统一计费输入
type CostInput struct {
	Ctx                       context.Context
	Model                     string
	GroupID                   *int64 // 用于渠道定价查找
	Tokens                    UsageTokens
	RequestCount              int    // 按次计费时使用
	SizeTier                  string // 按次/图片模式的层级标签（"1K","2K","4K","HD" 等）
	RateMultiplier            float64
	ServiceTier               string                // "priority","flex","" 等
	EnableThinking            bool                  // 本次请求是否处于思考模式（含默认开启）；仅对带 ThinkingOutputPricePerToken 的模型改变输出价
	Resolver                  *ModelPricingResolver // 定价解析器
	Resolved                  *ResolvedPricing      // 可选：预解析的定价结果（避免重复 Resolve 调用）
	LongContextBillingEnabled *bool
	// BillingAt is the wall-clock instant used for provider time-of-day pricing
	// (DeepSeek peak-valley). Zero → timezone.Now() at billing time.
	BillingAt time.Time
}

// CalculateCostUnified 统一计费入口，支持三种计费模式。
// 使用 ModelPricingResolver 解析定价，然后根据 BillingMode 分发计算。
func (s *BillingService) CalculateCostUnified(input CostInput) (*CostBreakdown, error) {
	if input.Resolver == nil {
		// 无 Resolver，回退到旧路径
		applyLongContextBilling := true
		if input.LongContextBillingEnabled != nil {
			applyLongContextBilling = *input.LongContextBillingEnabled
		}
		return s.calculateCostInternalWithPolicy(
			input.Model,
			input.Tokens,
			input.RateMultiplier,
			input.ServiceTier,
			nil,
			applyLongContextBilling,
		)
	}

	// 优先使用预解析结果，避免重复 Resolve 调用
	resolved := input.Resolved
	if resolved == nil {
		resolved = input.Resolver.Resolve(input.Ctx, PricingInput{
			Model:   input.Model,
			GroupID: input.GroupID,
		})
	}

	// 保存时强制 > 0；若仍有负数泄漏（缓存/迁移残留），按 0 处理避免按 1x 误扣。
	if input.RateMultiplier < 0 {
		input.RateMultiplier = 0
	}

	var breakdown *CostBreakdown
	var err error
	switch resolved.Mode {
	case BillingModePerRequest, BillingModeImage:
		breakdown, err = s.calculatePerRequestCost(resolved, input)
	default: // BillingModeToken
		breakdown, err = s.calculateTokenCost(resolved, input)
	}
	if err == nil && breakdown != nil {
		breakdown.BillingMode = string(resolved.Mode)
		if breakdown.BillingMode == "" {
			breakdown.BillingMode = string(BillingModeToken)
		}
	}
	return breakdown, err
}

// calculateTokenCost 按 token 区间计费
func (s *BillingService) calculateTokenCost(resolved *ResolvedPricing, input CostInput) (*CostBreakdown, error) {
	totalContext := input.Tokens.InputTokens + input.Tokens.CacheCreationTokens + input.Tokens.CacheReadTokens

	pricing := input.Resolver.GetIntervalPricing(resolved, totalContext)
	if pricing == nil {
		return nil, fmt.Errorf("no pricing available for model: %s: %w", input.Model, ErrModelPricingUnavailable)
	}

	at := input.BillingAt
	if at.IsZero() {
		at = timezone.Now()
	}
	pricing = s.applyModelSpecificPricingPolicy(input.Model, pricing)
	pricing = tkApplyDeepSeekPeakValleyPricing(input.Model, pricing, at, resolved.Source)

	// 长上下文定价仅在无区间定价时应用（区间定价已包含上下文分层）
	applyLongCtx := len(resolved.Intervals) == 0
	if input.LongContextBillingEnabled != nil {
		applyLongCtx = applyLongCtx && *input.LongContextBillingEnabled
	}

	return s.computeTokenBreakdown(pricing, input.Tokens, input.RateMultiplier, input.ServiceTier, input.EnableThinking, applyLongCtx), nil
}

// computeTokenBreakdown 是 token 计费的核心逻辑，由 calculateTokenCost 和 calculateCostInternal 共用。
// applyLongCtx 控制是否检查长上下文定价（区间定价已自含上下文分层，不需要额外应用）。
func (s *BillingService) computeTokenBreakdown(
	pricing *ModelPricing, tokens UsageTokens,
	rateMultiplier float64, serviceTier string,
	enableThinking bool,
	applyLongCtx bool,
) *CostBreakdown {
	// 保存时强制 > 0；若仍有负数泄漏，按 0 处理避免按 1x 误扣。
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}

	inputPrice := pricing.InputPricePerToken
	outputPrice := pricing.OutputPricePerToken
	// 思考模式输出溢价：上游（如 Alibaba DashScope qwen3 dense）对同一 model id 在
	// enable_thinking 开启时按更高的输出价计费。仅当该模型配了 ThinkingOutputPricePerToken
	// (>0) 才生效，故对其它模型恒为 no-op。serviceTier priority 价（下方）仍可继续覆盖。
	if enableThinking && pricing.ThinkingOutputPricePerToken > 0 {
		outputPrice = pricing.ThinkingOutputPricePerToken
	}
	cacheReadPrice := pricing.CacheReadPricePerToken
	cacheCreationPrice := pricing.CacheCreationPricePerToken
	cacheCreationMultiplier := 1.0
	tierMultiplier := 1.0

	if usePriorityServiceTierPricing(serviceTier, pricing) {
		if pricing.InputPricePerTokenPriority > 0 {
			inputPrice = pricing.InputPricePerTokenPriority
		}
		if pricing.OutputPricePerTokenPriority > 0 {
			outputPrice = pricing.OutputPricePerTokenPriority
		}
		if pricing.CacheReadPricePerTokenPriority > 0 {
			cacheReadPrice = pricing.CacheReadPricePerTokenPriority
		}
		if pricing.CacheCreationPricePerTokenPriority > 0 {
			cacheCreationPrice = pricing.CacheCreationPricePerTokenPriority
		}
	} else {
		tierMultiplier = serviceTierCostMultiplier(serviceTier)
	}

	longContextPricingEligible := applyLongCtx && s.shouldApplySessionLongContextPricing(tokens, pricing)
	var baselineCost *CostBreakdown
	if longContextPricingEligible {
		baselineCost = s.computeTokenBreakdown(pricing, tokens, rateMultiplier, serviceTier, false, false)
		inputPrice *= pricing.LongContextInputMultiplier
		outputPrice *= pricing.LongContextOutputMultiplier
		// 缓存读取本质上是输入侧的复用，应与 input 一同应用长上下文倍率；
		// 否则 cache hit 越多，少计的费用越多（见 #2293）。
		cacheReadPrice *= pricing.LongContextInputMultiplier
		// 缓存创建（cache_write）也是输入侧操作，三档价格（标准 / 5m / 1h）
		// 都通过 computeCacheCreationCost 直接读取 pricing.*，不会经过这里
		// 的倍率修改，因此显式向下传一个倍率，避免长上下文场景下被漏乘。
		cacheCreationMultiplier = pricing.LongContextInputMultiplier
	}

	bd := &CostBreakdown{}
	// 分离图片输入 token 与文本输入 token（多模态 embedding 等图文不同价场景）。
	// ImageInputTokens 为 0 时（绝大多数 chat/vision 流量）走原始单价路径，行为不变。
	if tokens.ImageInputTokens > 0 {
		imageInputTokens := tokens.ImageInputTokens
		textInputTokens := tokens.InputTokens - imageInputTokens
		if textInputTokens < 0 {
			textInputTokens = 0
			imageInputTokens = tokens.InputTokens
		}
		imageInputPrice := pricing.ImageInputPricePerToken
		if imageInputPrice == 0 {
			// 未配置图片输入档时回退到文本 input 价（已含 priority / 长上下文调整）
			imageInputPrice = inputPrice
		}
		bd.InputCost = float64(textInputTokens)*inputPrice + float64(imageInputTokens)*imageInputPrice
	} else {
		bd.InputCost = float64(tokens.InputTokens) * inputPrice
	}

	// 分离图片输出 token 与文本输出 token
	textOutputTokens := tokens.OutputTokens - tokens.ImageOutputTokens
	if textOutputTokens < 0 {
		textOutputTokens = 0
	}
	bd.OutputCost = float64(textOutputTokens) * outputPrice

	// 图片输出 token 费用（独立费率）
	if tokens.ImageOutputTokens > 0 {
		imgPrice := pricing.ImageOutputPricePerToken
		if imgPrice == 0 && !pricing.ImageOutputPriceExplicit {
			imgPrice = outputPrice
		}
		bd.ImageOutputCost = float64(tokens.ImageOutputTokens) * imgPrice
	}

	// 缓存创建费用
	bd.CacheCreationCost = s.computeCacheCreationCost(pricing, tokens, cacheCreationPrice, cacheCreationMultiplier)

	bd.CacheReadCost = float64(tokens.CacheReadTokens) * cacheReadPrice

	if tierMultiplier != 1.0 {
		bd.InputCost *= tierMultiplier
		bd.OutputCost *= tierMultiplier
		bd.ImageOutputCost *= tierMultiplier
		bd.CacheCreationCost *= tierMultiplier
		bd.CacheReadCost *= tierMultiplier
	}

	bd.TotalCost = bd.InputCost + bd.OutputCost + bd.ImageOutputCost +
		bd.CacheCreationCost + bd.CacheReadCost
	bd.ActualCost = bd.TotalCost * rateMultiplier
	bd.LongContextBillingApplied = baselineCost != nil && bd.ActualCost > baselineCost.ActualCost

	return bd
}

// computeCacheCreationCost 计算缓存创建费用（支持 5m/1h 分类或标准计费）。
// multiplier 用于长上下文等场景下的整体价格缩放（普通调用传 1.0 即可）。
func (s *BillingService) computeCacheCreationCost(pricing *ModelPricing, tokens UsageTokens, price, multiplier float64) float64 {
	if pricing.SupportsCacheBreakdown && (pricing.CacheCreation5mPrice > 0 || pricing.CacheCreation1hPrice > 0) {
		if tokens.CacheCreation5mTokens == 0 && tokens.CacheCreation1hTokens == 0 && tokens.CacheCreationTokens > 0 {
			// API 未返回 ephemeral 明细，回退到全部按 5m 单价计费
			return float64(tokens.CacheCreationTokens) * pricing.CacheCreation5mPrice * multiplier
		}
		return float64(tokens.CacheCreation5mTokens)*pricing.CacheCreation5mPrice*multiplier +
			float64(tokens.CacheCreation1hTokens)*pricing.CacheCreation1hPrice*multiplier
	}
	return float64(tokens.CacheCreationTokens) * price * multiplier
}

// calculatePerRequestCost 按次/图片计费
func (s *BillingService) calculatePerRequestCost(resolved *ResolvedPricing, input CostInput) (*CostBreakdown, error) {
	count := input.RequestCount
	if count <= 0 {
		count = 1
	}

	var unitPrice float64

	if input.SizeTier != "" {
		unitPrice = input.Resolver.GetRequestTierPrice(resolved, input.SizeTier)
	}

	if unitPrice == 0 {
		totalContext := input.Tokens.InputTokens + input.Tokens.CacheCreationTokens + input.Tokens.CacheReadTokens
		unitPrice = input.Resolver.GetRequestTierPriceByContext(resolved, totalContext)
	}

	// 回退到默认按次价格
	if unitPrice == 0 {
		unitPrice = resolved.DefaultPerRequestPrice
	}

	totalCost := unitPrice * float64(count)
	actualCost := totalCost * input.RateMultiplier

	return &CostBreakdown{
		TotalCost:  totalCost,
		ActualCost: actualCost,
	}, nil
}

// CalculateCost 计算使用费用
func (s *BillingService) CalculateCost(model string, tokens UsageTokens, rateMultiplier float64) (*CostBreakdown, error) {
	return s.calculateCostInternal(model, tokens, rateMultiplier, "", nil)
}

func (s *BillingService) CalculateCostWithServiceTier(model string, tokens UsageTokens, rateMultiplier float64, serviceTier string) (*CostBreakdown, error) {
	return s.calculateCostInternal(model, tokens, rateMultiplier, serviceTier, nil)
}

func (s *BillingService) calculateCostWithServiceTierPolicy(
	model string,
	tokens UsageTokens,
	rateMultiplier float64,
	serviceTier string,
	longContextBillingEnabled bool,
) (*CostBreakdown, error) {
	return s.calculateCostInternalWithPolicy(model, tokens, rateMultiplier, serviceTier, nil, longContextBillingEnabled)
}

func (s *BillingService) calculateCostInternal(model string, tokens UsageTokens, rateMultiplier float64, serviceTier string, channelPricing *ChannelModelPricing) (*CostBreakdown, error) {
	return s.calculateCostInternalWithPolicy(model, tokens, rateMultiplier, serviceTier, channelPricing, true)
}

func (s *BillingService) calculateCostInternalWithPolicy(
	model string,
	tokens UsageTokens,
	rateMultiplier float64,
	serviceTier string,
	channelPricing *ChannelModelPricing,
	longContextBillingEnabled bool,
) (*CostBreakdown, error) {
	var pricing *ModelPricing
	var err error
	if channelPricing != nil {
		pricing, err = s.GetModelPricingWithChannel(model, channelPricing)
	} else {
		pricing, err = s.GetModelPricing(model)
	}
	if err != nil {
		return nil, err
	}

	source := PricingSourceRegistry
	if channelPricing != nil {
		source = PricingSourceChannel
	}
	pricing = s.applyModelSpecificPricingPolicy(model, pricing)
	pricing = tkApplyDeepSeekPeakValleyPricing(model, pricing, timezone.Now(), source)

	// 旧路径始终检查长上下文定价（无区间定价概念）。该路径不携带 enable_thinking
	// （仅 CalculateCostUnified/CostInput 链路透传思考模式），故按非思考计费。
	return s.computeTokenBreakdown(pricing, tokens, rateMultiplier, serviceTier, false, longContextBillingEnabled), nil
}

func (s *BillingService) applyModelSpecificPricingPolicy(model string, pricing *ModelPricing) *ModelPricing {
	// Pricing policy is data-owned. Long-context thresholds, multipliers, cache
	// prices, and service-tier rates must be present on the resolved registry row
	// (or a scoped channel override); this method intentionally performs no
	// numeric completion or model-family fallback.
	return pricing
}

func (s *BillingService) shouldApplySessionLongContextPricing(tokens UsageTokens, pricing *ModelPricing) bool {
	if pricing == nil || pricing.LongContextInputThreshold <= 0 {
		return false
	}
	if pricing.LongContextInputMultiplier <= 1 && pricing.LongContextOutputMultiplier <= 1 {
		return false
	}
	totalInputTokens := tokens.InputTokens + tokens.CacheCreationTokens + tokens.CacheReadTokens
	return totalInputTokens > pricing.LongContextInputThreshold
}

// CalculateCostWithConfig 使用配置中的默认倍率计算费用
func (s *BillingService) CalculateCostWithConfig(model string, tokens UsageTokens) (*CostBreakdown, error) {
	multiplier := s.cfg.Default.RateMultiplier
	if multiplier <= 0 {
		multiplier = 1.0
	}
	return s.CalculateCost(model, tokens, multiplier)
}

// CalculateCostWithLongContext 计算费用，支持长上下文双倍计费
// threshold: 阈值（如 200000），超过此值的部分按 extraMultiplier 倍计费
// extraMultiplier: 超出部分的倍率（如 2.0 表示双倍）
//
// 示例：缓存 210k + 输入 10k = 220k，阈值 200k，倍率 2.0
// 拆分为：范围内 (200k, 0) + 范围外 (10k, 10k)
// 范围内正常计费，范围外 × 2 计费
func (s *BillingService) CalculateCostWithLongContext(model string, tokens UsageTokens, rateMultiplier float64, threshold int, extraMultiplier float64) (*CostBreakdown, error) {
	// 未启用长上下文计费，直接走正常计费
	if threshold <= 0 || extraMultiplier <= 1 {
		return s.CalculateCost(model, tokens, rateMultiplier)
	}

	// 计算总输入 token（缓存读取 + 新输入）
	total := tokens.CacheReadTokens + tokens.InputTokens
	if total <= threshold {
		return s.CalculateCost(model, tokens, rateMultiplier)
	}

	// 拆分成范围内和范围外
	var inRangeCacheTokens, inRangeInputTokens int
	var outRangeCacheTokens, outRangeInputTokens int

	if tokens.CacheReadTokens >= threshold {
		// 缓存已超过阈值：范围内只有缓存，范围外是超出的缓存+全部输入
		inRangeCacheTokens = threshold
		inRangeInputTokens = 0
		outRangeCacheTokens = tokens.CacheReadTokens - threshold
		outRangeInputTokens = tokens.InputTokens
	} else {
		// 缓存未超过阈值：范围内是全部缓存+部分输入，范围外是剩余输入
		inRangeCacheTokens = tokens.CacheReadTokens
		inRangeInputTokens = threshold - tokens.CacheReadTokens
		outRangeCacheTokens = 0
		outRangeInputTokens = tokens.InputTokens - inRangeInputTokens
	}

	// 范围内部分：正常计费
	inRangeTokens := UsageTokens{
		InputTokens:           inRangeInputTokens,
		OutputTokens:          tokens.OutputTokens, // 输出只算一次
		CacheCreationTokens:   tokens.CacheCreationTokens,
		CacheReadTokens:       inRangeCacheTokens,
		CacheCreation5mTokens: tokens.CacheCreation5mTokens,
		CacheCreation1hTokens: tokens.CacheCreation1hTokens,
		ImageOutputTokens:     tokens.ImageOutputTokens,
	}
	inRangeCost, err := s.CalculateCost(model, inRangeTokens, rateMultiplier)
	if err != nil {
		return nil, err
	}

	// 范围外部分：× extraMultiplier 计费
	outRangeTokens := UsageTokens{
		InputTokens:     outRangeInputTokens,
		CacheReadTokens: outRangeCacheTokens,
	}
	outRangeCost, err := s.CalculateCost(model, outRangeTokens, rateMultiplier*extraMultiplier)
	if err != nil {
		return inRangeCost, fmt.Errorf("out-range cost: %w", err)
	}

	// 合并成本
	return &CostBreakdown{
		InputCost:                 inRangeCost.InputCost + outRangeCost.InputCost,
		OutputCost:                inRangeCost.OutputCost,
		ImageOutputCost:           inRangeCost.ImageOutputCost,
		CacheCreationCost:         inRangeCost.CacheCreationCost,
		CacheReadCost:             inRangeCost.CacheReadCost + outRangeCost.CacheReadCost,
		TotalCost:                 inRangeCost.TotalCost + outRangeCost.TotalCost,
		ActualCost:                inRangeCost.ActualCost + outRangeCost.ActualCost,
		LongContextBillingApplied: outRangeCost.ActualCost > 0,
	}, nil
}

// ListSupportedModels 列出所有支持的模型（现在总是返回true，因为有模糊匹配）
func (s *BillingService) ListSupportedModels() []string {
	models := make([]string, 0, len(loadTKPricingOverlay()))
	for model := range loadTKPricingOverlay() {
		models = append(models, model)
	}
	return models
}

// IsModelSupported 检查模型是否支持（Claude 家族由 registry alias policy 覆盖）
func (s *BillingService) IsModelSupported(model string) bool {
	// 所有 Claude 模型都有 registry family alias 支持。
	modelLower := strings.ToLower(model)
	return strings.Contains(modelLower, "claude") ||
		strings.Contains(modelLower, "opus") ||
		strings.Contains(modelLower, "sonnet") ||
		strings.Contains(modelLower, "haiku")
}

// GetEstimatedCost 估算费用（用于前端展示）
func (s *BillingService) GetEstimatedCost(model string, estimatedInputTokens, estimatedOutputTokens int) (float64, error) {
	tokens := UsageTokens{
		InputTokens:  estimatedInputTokens,
		OutputTokens: estimatedOutputTokens,
	}

	breakdown, err := s.CalculateCostWithConfig(model, tokens)
	if err != nil {
		return 0, err
	}

	return breakdown.ActualCost, nil
}

// ImagePriceConfig 图片计费配置
type ImagePriceConfig struct {
	Price1K *float64 // 1K 尺寸价格（nil 表示使用默认值）
	Price2K *float64 // 2K 尺寸价格（nil 表示使用默认值）
	Price4K *float64 // 4K 尺寸价格（nil 表示使用默认值）
}

// VideoPriceConfig 视频生成计费配置。所有价格均为**每秒**单价（USD/s），与 xAI 官方计费口径一致。
type VideoPriceConfig struct {
	Price480P  *float64 // 480p 每秒价格（nil 表示使用默认值）
	Price720P  *float64 // 720p 每秒价格（nil 表示使用默认值）
	Price1080P *float64 // 1080p 每秒价格（nil 表示使用默认值）
}

// CalculateWebSearchCost 计算 Codex alpha/search 网页搜索按次费用。
// callCount: 搜索调用次数（每次请求为 1）
// groupPrice: 分组配置的单次价格（nil 表示使用 registry policy；0 表示免费）
// rateMultiplier: 分组费率倍数
func (s *BillingService) CalculateWebSearchCost(callCount int, groupPrice *float64, rateMultiplier float64) *CostBreakdown {
	if callCount <= 0 {
		return &CostBreakdown{}
	}
	unitPrice := tkRegistryWebSearchPricePerCall()
	if groupPrice != nil && *groupPrice >= 0 {
		unitPrice = *groupPrice
	}
	totalCost := unitPrice * float64(callCount)

	// 应用倍率（保存时强制 > 0；负数按 0 处理避免按 1x 误扣）
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}
	return &CostBreakdown{
		TotalCost:   totalCost,
		ActualCost:  totalCost * rateMultiplier,
		BillingMode: string(BillingModePerRequest),
	}
}

// CalculateImageCost 计算图片生成费用
// model: 请求的模型名称（用于解析 registry owner）
// imageSize: 图片尺寸 "1K", "2K", "4K"
// imageCount: 生成的图片数量
// groupConfig: 分组配置的价格（可能为 nil，表示使用默认值）
// rateMultiplier: 费率倍数
func (s *BillingService) CalculateImageCost(model string, imageSize string, imageCount int, groupConfig *ImagePriceConfig, rateMultiplier float64) *CostBreakdown {
	if imageCount <= 0 {
		return &CostBreakdown{}
	}
	imageSize = NormalizeImageBillingTierOrDefault(imageSize)

	// 获取单价
	unitPrice := s.getImageUnitPrice(model, imageSize, groupConfig)

	// 计算总费用
	totalCost := unitPrice * float64(imageCount)

	// 应用倍率（保存时强制 > 0；负数按 0 处理避免按 1x 误扣）
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}
	actualCost := totalCost * rateMultiplier

	return &CostBreakdown{
		TotalCost:   totalCost,
		ActualCost:  actualCost,
		BillingMode: string(BillingModeImage),
	}
}

// CalculateVideoCost 计算视频生成费用（按秒计费，与上游官方口径对齐）。
// model: 请求的模型名称（用于获取默认价格）
// resolution: 视频分辨率 "480p", "720p", "1080p", "4k"（空则按模型默认输出档）
// videoCount: 生成的视频数量
// durationSeconds: 单个视频时长（秒），<=0 时按上游默认时长计
// groupConfig: 分组配置的每秒价格（可能为 nil，表示使用默认值）
// rateMultiplier: 费率倍数
// opts: 可选阶梯维度（分辨率×音频×图生附加费等）；nil 时使用各模型上游默认
func (s *BillingService) CalculateVideoCost(model string, resolution string, videoCount int, durationSeconds int, groupConfig *VideoPriceConfig, rateMultiplier float64, opts *VideoBillingOptions) *CostBreakdown {
	if videoCount <= 0 {
		return &CostBreakdown{}
	}
	resolution = NormalizeVideoBillingResolutionForModel(model, resolution)
	if durationSeconds <= 0 {
		if tkIsGrokImagineVideoModel(model) {
			durationSeconds = NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds)
		} else {
			durationSeconds = 1
		}
	} else {
		durationSeconds = NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds)
	}

	perSecondPrice := s.getVideoUnitPrice(model, resolution, groupConfig, opts)
	totalCost := perSecondPrice * float64(durationSeconds) * float64(videoCount)

	if rateMultiplier < 0 {
		rateMultiplier = 0
	}
	actualCost := totalCost * rateMultiplier

	return &CostBreakdown{
		TotalCost:   totalCost,
		ActualCost:  actualCost,
		BillingMode: string(BillingModeVideo),
	}
}

// getImageUnitPrice 获取图片单价
func (s *BillingService) getImageUnitPrice(model string, imageSize string, groupConfig *ImagePriceConfig) float64 {
	// Defensive normalization: when imageSize is empty/whitespace, mirror the
	// request-side default in normalizeOpenAIImageSizeTier ("" → "2K") so an
	// upstream pipeline that fails to propagate size into ForwardResult does
	// not silently skip group image-price overrides nor mis-classify the
	// default tier as 1K. See upstream Wei-Shaw/sub2api#2539.
	normalizedSize := imageSize
	if strings.TrimSpace(normalizedSize) == "" {
		normalizedSize = "2K"
	}

	// 优先使用分组配置的价格
	if groupConfig != nil {
		switch normalizedSize {
		case "1K":
			if groupConfig.Price1K != nil {
				return *groupConfig.Price1K
			}
		case "2K":
			if groupConfig.Price2K != nil {
				return *groupConfig.Price2K
			}
		case "4K":
			if groupConfig.Price4K != nil {
				return *groupConfig.Price4K
			}
		}
	}

	// Resolve the registry owner. Group pricing above is the only scoped override.
	return s.getDefaultImagePrice(model, normalizedSize)
}

func (s *BillingService) getVideoUnitPrice(model string, resolution string, groupConfig *VideoPriceConfig, opts *VideoBillingOptions) float64 {
	if groupConfig != nil {
		switch resolution {
		case VideoBillingResolution480P:
			if groupConfig.Price480P != nil {
				return *groupConfig.Price480P
			}
		case VideoBillingResolution720P:
			if groupConfig.Price720P != nil {
				return *groupConfig.Price720P
			}
		case VideoBillingResolution1080P:
			if groupConfig.Price1080P != nil {
				return *groupConfig.Price1080P
			}
		}
	}

	if price, ok := tkVideoUnitPriceUSD(model, resolution, opts); ok && price > 0 {
		return price
	}

	return s.getDefaultVideoPrice(model, resolution)
}

// getDefaultImagePrice resolves the global registry owner price.
func (s *BillingService) getDefaultImagePrice(model string, imageSize string) float64 {
	if price, ok := getDefaultGrokImagineImagePrice(model, imageSize); ok {
		return price
	}

	basePrice := 0.0

	// Read the registry owner even when the service dependency is absent (tests and
	// defensive startup paths); never substitute a Go numeric price.
	pricing := tkOverlayLiteLLMModelPricing(model)
	if s.pricingService != nil {
		pricing = s.pricingService.GetModelPricing(model)
	}
	if pricing != nil && pricing.OutputCostPerImage > 0 {
		basePrice = pricing.OutputCostPerImage
	}

	// TK: Imagen bills at its FLAT official per-image price. Google prices Imagen
	// as a single $/image per quality variant with NO 1K/2K/4K generation tier, so
	// the size-tier multiplier below (real only for genuine pixel-size tiers such
	// as Seedream) must not apply. Imagen requests carry ratio codes (no size) and
	// default to "2K", which silently marked them up ×1.5 — this exemption removes
	// that. Routes through the same function as the pre-flight HOLD, so settle and
	// hold stay consistent. See tkIsFlatPerImageModel (billing_service_tk_flat_image.go).
	if tkIsFlatPerImageModel(model) {
		return basePrice
	}

	// 2K 尺寸 1.5 倍，4K 尺寸翻倍
	if imageSize == "2K" {
		return basePrice * 1.5
	}
	if imageSize == "4K" {
		return basePrice * 2
	}

	return basePrice
}

func (s *BillingService) getDefaultVideoPrice(model string, resolution string) float64 {
	if price, ok := tkOverlayVideoUnitPriceUSD(model, resolution, nil); ok {
		return price
	}

	if s.pricingService != nil {
		if pricing := s.pricingService.GetModelPricing(model); pricing != nil && pricing.OutputCostPerSecond > 0 {
			return pricing.OutputCostPerSecond
		}
	}
	return 0
}

func getDefaultGrokImagineImagePrice(model string, imageSize string) (float64, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	ownerModel := ""
	switch model {
	case "grok-imagine-image-quality":
		ownerModel = "grok-imagine-image-quality"
	case "grok-imagine", "grok-imagine-image", "grok-imagine-edit":
		ownerModel = "grok-imagine-image"
	default:
		return 0, false
	}

	pricing := tkOverlayLiteLLMModelPricing(ownerModel)
	if pricing == nil {
		return 0, false
	}
	price := pricing.OutputCostPerImage
	switch NormalizeImageBillingTierOrDefault(imageSize) {
	case ImageBillingSize1K:
		if pricing.ImagePrice1K > 0 {
			price = pricing.ImagePrice1K
		}
	case ImageBillingSize2K:
		if pricing.ImagePrice2K > 0 {
			price = pricing.ImagePrice2K
		}
	case ImageBillingSize4K:
		if pricing.ImagePrice4K > 0 {
			price = pricing.ImagePrice4K
		}
	}
	return price, price > 0
}

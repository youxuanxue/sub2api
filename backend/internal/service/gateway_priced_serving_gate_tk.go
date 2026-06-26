package service

// TokenKey: runtime "priced-or-it-doesnt-ship" serving-admission gate (v1).
//
// 设计：docs/approved/priced-or-it-doesnt-ship.md。一条规矩、全平台（按启用集灰度）：
// billing model 解析后、上游转发前，若该模型在价格 catalog 里无可解析价
// (`!PricingCatalogService.IsModelPriced`)，则 fail-closed 返回 404（外形与上游
// 「模型不可用」字节对齐，内部子码 model_not_priced 只进 body/日志），而非按 $0 服务。
// 这堵住 native 平台「空 model_mapping = catch-all 透传」会把任意未定价 id 按 $0
// 端给付费客户的漏洞（CI-time A1 guard 的运行期对应）。
//
// v1 范围（架构师拍板，docs §4/§8-D4 增量阶梯）：**闸 + 拒绝时触发既有缺价告警**。
// 拒绝时复用既有 PricingMissingNotifier（gateway_service_tk_served_zero_cost.go 同一
// 信号面）发飞书卡片，让运维用现成 `apply-pricing-hotfix.py` 补价——这是 v1 的「自动
// 定价通路」（人在环、5 秒批），满足设计 R4「闸非空转」。v2（litellm 一键确认 + Go
// overlay 写器）、v3（官方价全自动）是 fast-follow，不在本文件。
//
// 为什么不放 handler 包：4 条注入点都在 service 深处（model mapping 发生在 handler
// 返回后），把 helper 放 service 既避免 handler→service 反向依赖，又能直接拿到 gin
// context（取 api_key/group 做告警）、catalog 谓词与 notifier。上游 handler 文件零改动。
//
// 关键不变量（SSE pre-flight）：闸**必须在首字节前**触发——流式途中无法补 404。所有
// 调用点都在 billing model 解析后、转发/流开始前（streamStarted 之前），见各路线注入。

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	// tkPricedServingGateSubcode 是拒绝的内部子码：只进 body（能放则放）+ 结构化日志，
	// **绝不**进 HTTP 状态行（状态统一 404）。priced-vs-unknown 是运维关切，不暴露给
	// 客户分支用的状态码（D1：避免 SDK 把它当鉴权失败重试）。
	tkPricedServingGateSubcode = "model_not_priced"
	// tkPricedServingGateRejectReason 是拒绝事件喂给 PricingMissingNotifier 的 reason，
	// 区别于 served_zero_cost 的 "unpriced"（已服务零计费）/"negative_multiplier"。卡片
	// 文案在 pricingMissingReasonLabel 映射。
	tkPricedServingGateRejectReason = "gate_rejected_unpriced"
	// tkPricedServingGateMessage 是返回给客户端的人类可读文案（三平台共用），刻意贴近
	// 上游「未知模型」措辞，让客户端 SDK 走既有未知模型路径。
	tkPricedServingGateMessage = "model not available"
)

// tkPricedServingGateRejected 是闸内部的判定结果：true = 应拒绝（未定价且平台已启用）。
// 拆成独立纯函数便于单测断言「开/关 × 定价/未定价」矩阵，不依赖 gin/notifier。
//
// 短路顺序（性能 + 正确性）：
//  1. setting 未启用该平台 → false（放行，零 catalog 查表开销）；
//  2. catalog 谓词 tkIsModelEffectivelyPriced → 命中即放行；
//  3. 否则拒绝。
//
// 用 tkIsModelEffectivelyPriced（**非** 裸 IsModelPriced）是 R3 一致性的关键：catalog
// 里有条目但 token 价全 0 的模型，IsModelPriced 返 true、但 billing 的 GetModelPricing
// 经 tkIsEffectivelyUnpriced 仍返 ErrModelPricingUnavailable 按 $0 记账——若闸用裸成员
// 谓词就会放过它、形同虚设。tkIsModelEffectivelyPriced 与计费侧同语义（见
// pricing_catalog_membership_tk.go），R3 测试钉死两者在候选集上等价。
func tkPricedServingGateRejected(
	ctx context.Context,
	catalog *PricingCatalogService,
	setting *SettingService,
	billingModel, platform string,
) bool {
	if setting == nil || catalog == nil {
		// 依赖未注入（降级/测试接线）→ 永不拒绝。闸是叠加的减法，缺依赖必须 fail-open，
		// 绝不因接线问题误拒真实流量。
		return false
	}
	if !setting.IsPricedServingGateEnabled(ctx, platform) {
		return false
	}
	if catalog.tkIsModelEffectivelyPriced(billingModel, platform) {
		return false
	}
	return true
}

// tkCheckPricedServingGate 是各路线注入的统一闸点。返回 true = 放行（继续转发）；
// false = 已拒绝（已写 404 响应 + 触发告警），调用方必须立即 return 不再转发。
//
// requestedModel 用于告警样例展示（客户端原始模型名），billingModel 是判定键 +
// 拒绝文案里点名的模型。platform 是 account.Platform（gemini 即含 vertex）。
//
// nil-safe：catalog/setting/c 任一为 nil 都安全放行（见 tkPricedServingGateRejected）。
func tkCheckPricedServingGate(
	ctx context.Context,
	catalog *PricingCatalogService,
	setting *SettingService,
	notifier PricingMissingNotifier,
	c *gin.Context,
	platform, billingModel, requestedModel string,
) bool {
	if !tkPricedServingGateRejected(ctx, catalog, setting, billingModel, platform) {
		return true
	}
	tkWritePricedServingGateRejection(c, platform, billingModel)
	tkLogAndNotifyPricedServingGateRejection(c, notifier, platform, billingModel, requestedModel)
	return false
}

// tkWritePricedServingGateRejection 按平台字节对齐写 404 拒绝 body。HTTP 状态统一 404，
// 子码 model_not_priced 只在能放下的平台进 body（OpenAI/NewAPI 有 code 字段；Anthropic
// 无 code 字段，子码只走日志；Gemini 是 numeric-code 形）。
func tkWritePricedServingGateRejection(c *gin.Context, platform, billingModel string) {
	if c == nil {
		return
	}
	MarkResponseCommitted(c)
	switch tkPricedServingGatePlatformFamily(platform) {
	case tkGateFamilyAnthropic:
		// Anthropic 形：{type:error, error:{type:not_found_error, message}}（无 code 字段）。
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "not_found_error",
				"message": tkPricedServingGateMessage,
			},
		})
	case tkGateFamilyGemini:
		// Gemini 形：googleError(404) → {error:{code:404, message, status:NOT_FOUND}}。
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"code":    http.StatusNotFound,
				"message": tkPricedServingGateMessage,
				"status":  googleapi.HTTPStatusToGoogleStatus(http.StatusNotFound),
			},
		})
	default:
		// OpenAI/NewAPI 形：{error:{type:invalid_request_error, code:model_not_priced, message}}。
		// 子码进 body 的 code 字段（OpenAI errorResponse helper 本身不带 code，这里直接写）。
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"code":    tkPricedServingGateSubcode,
				"message": tkPricedServingGateMessage,
			},
		})
	}
}

// tkLogAndNotifyPricedServingGateRejection 写结构化日志 priced_serving_gate.rejected
// （与 served_zero_cost 对称）并触发既有 PricingMissingNotifier（v1 自动定价通路：让
// 运维收到「模型 X 未定价被拒、去补价」飞书卡片）。
func tkLogAndNotifyPricedServingGateRejection(
	c *gin.Context,
	notifier PricingMissingNotifier,
	platform, billingModel, requestedModel string,
) {
	// getAPIKeyFromContext dereferences c.Get; a nil gin context (degraded/test
	// wiring) would panic, so guard here. Production always has a non-nil c.
	var apiKey *APIKey
	if c != nil {
		apiKey = getAPIKeyFromContext(c)
	}
	group := apiKeyGroup(apiKey)

	fields := []zap.Field{
		zap.String("component", "service.gateway"),
		zap.String("subcode", tkPricedServingGateSubcode),
		zap.String("platform", platform),
		zap.String("billing_model", billingModel),
		zap.String("requested_model", requestedModel),
	}
	if apiKey != nil {
		fields = append(fields, zap.Int64("api_key_id", apiKey.ID))
	}
	if group != nil {
		fields = append(fields,
			zap.Int64("group_id", group.ID),
			zap.String("group_name", group.Name),
		)
	}
	logger.L().With(fields...).Warn("priced_serving_gate.rejected")

	if notifier == nil {
		return
	}
	ev := PricingMissingEvent{
		Reason:         tkPricedServingGateRejectReason,
		Platform:       platform,
		BillingModel:   billingModel,
		RequestedModel: requestedModel,
	}
	if apiKey != nil {
		ev.APIKeyID = apiKey.ID
	}
	if group != nil {
		ev.GroupID = group.ID
		ev.GroupName = group.Name
		if ev.Platform == "" {
			ev.Platform = group.Platform
		}
	}
	notifier.NotifyPricingMissing(ev)
}

// tkPricedServingGatePlatformFamily 把 account.Platform 归到三种拒绝 body 形态之一。
type tkGateFamily int

const (
	tkGateFamilyOpenAI tkGateFamily = iota
	tkGateFamilyAnthropic
	tkGateFamilyGemini
)

func tkPricedServingGatePlatformFamily(platform string) tkGateFamily {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case PlatformAnthropic:
		return tkGateFamilyAnthropic
	case PlatformGemini:
		return tkGateFamilyGemini
	default:
		// openai / newapi / antigravity（compat 系）/ 未知 → OpenAI 形。首发启用集只有
		// gemini，但其余平台逐个加入时此默认分支即生效。
		return tkGateFamilyOpenAI
	}
}

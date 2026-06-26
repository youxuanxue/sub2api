package service

// TokenKey: runtime "priced-or-it-doesnt-ship" serving-admission gate (v1).
//
// 设计：docs/approved/priced-or-it-doesnt-ship.md。一条规矩、全平台（按启用集灰度）：
// billing model 解析后、上游转发前，若该模型解析不出价（billing 神谕
// `BillingService.GetModelPricing` 返回 ErrModelPricingUnavailable），则 fail-closed
// 返回 404（外形与上游「模型不可用」字节对齐，内部子码 model_not_priced 只进 body/日志），
// 而非按 $0 服务。这堵住 native 平台「空 model_mapping = catch-all 透传」会把任意未定价 id
// 按 $0 端给付费客户的漏洞（CI-time A1 guard 的运行期对应）。
//
// 根因（评审 BLOCKER1/SHOULD-FIX1/2 的统一修法）：闸的判据是 **billing 真正用来决定记不记
// $0 的同一个调用、同一个键**——`GetModelPricing(billingKey)`，而非 catalog 成员影子谓词
// （tkIsModelEffectivelyPriced）。理由：billing 用 GetModelPricing 决定记不记 $0，闸必须用
// 同一个神谕，闸 ⟺ billing 才是构造性成立、永不漂移：
//   - billing 的 getFallbackPricing 对任意 gemini-*/claude-* family 兜底返有效价（防 $0 漏血），
//     闸走同一调用即自动继承 family 兜底 → 不再误拒新 gemini（SHOULD-FIX1）；
//   - GetModelPricing 解析全维度（priority/above1hr/image-token/intervals/media），闸走同一
//     调用即字段对齐，不再漏判 priority-only/interval-only 等有价模型（SHOULD-FIX2）；
//   - 闸键逐路线传 billing 将记账的确切键（native gemini/anthropic 是 originalModel，openai
//     native 是 mapped billingModel），闸/账同键 → 堵住「闸查 mapped=priced 放行、billing 查
//     original=unpriced 记 $0」的反向漏血（BLOCKER1）。
//
// 降级 fail-OPEN（SHOULD-FIX：防大面积宕机）：若 pricing 源整体降级，GetModelPricing 会对
// 一批模型返 unavailable → 闸 404 掉 100% 启用平台流量。我们区分「系统健康但该模型未定价→拒」
// 与「pricing 系统降级→放行」：拒绝前用一个常驻已定价 canary 模型探一次，若连它都解析为未定价，
// 判定 pricing 系统降级 → fail-open 放行。billing 本身降级 fail-open $0，闸不能把定价文件 glitch
// 变成整服务 404。
//
// v1 范围（架构师拍板，docs §4/§8-D4 增量阶梯）：**闸 + 拒绝时触发既有缺价告警**。
// 拒绝时复用既有 PricingMissingNotifier（gateway_service_tk_served_zero_cost.go 同一
// 信号面）发飞书卡片，让运维用现成 `apply-pricing-hotfix.py` 补价——这是 v1 的「自动
// 定价通路」（人在环、5 秒批），满足设计 R4「闸非空转」。v2（litellm 一键确认 + Go
// overlay 写器）、v3（官方价全自动）是 fast-follow，不在本文件。
//
// 为什么不放 handler 包：多条注入点都在 service 深处（model mapping 发生在 handler
// 返回后），把 helper 放 service 既避免 handler→service 反向依赖，又能直接拿到 gin
// context（取 api_key/group 做告警）、billing 神谕与 notifier。上游 handler 文件零改动。
//
// 关键不变量（SSE pre-flight）：闸**必须在首字节前**触发——流式途中无法补 404。所有
// 调用点都在 billing model 解析后、转发/流开始前（streamStarted 之前），见各路线注入。
// 零计费 pre-flight 操作（如 gemini countTokens）必须在闸前 action 短路豁免（docs §4）：
// 它零漏血面、且契约是永不硬失败，对它 404 毫无收益且破契约。

import (
	"context"
	"errors"
	"net/http"

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
	// tkPricedServingGateMessage 是返回给客户端的人类可读文案（各 wire 协议共用），刻意贴近
	// 上游「未知模型」措辞，让客户端 SDK 走既有未知模型路径。
	tkPricedServingGateMessage = "model not available"
)

// tkBillingPricingResolver 是闸的判据：billing 真正用来决定记不记 $0 的同一个调用。
// 形如 BillingService.GetModelPricing —— 返回 (pricing, nil) = 有价；返回包裹
// ErrModelPricingUnavailable 的 err = 未定价（billing 会记 $0）。闸注入它而非整个
// *BillingService，便于各路线统一接线 + 单测直接喂一个 stub resolver。
type tkBillingPricingResolver func(model string) (*ModelPricing, error)

// tkChannelPricingProbe 报告某 group 是否对 model 配了【渠道价】(channel_model_pricing)，
// 精确镜像 billing 计费路径的 resolveChannelPricing：`resolver.Resolve(...).Source ==
// PricingSourceChannel` 才算。billing 对 per-request/image 模式的渠道价、以及覆盖在缺失基础价
// 之上的 token 渠道价都按【非零】计费，而 GetModelPricing 不带 group、根本查不到这些渠道价 →
// 闸若只问 GetModelPricing，会误拒「渠道有价但基础价缺」的可计费模型（复审 BLOCKER B1：
// 闸键 == 账键但闸的【价源】不全）。闸补这道渠道价探测后，「闸 ⟺ billing」对两个价源都构造性
// 成立。nil = 不注入（退化为仅基础价判定，安全 fail-open 方向）。
type tkChannelPricingProbe func(ctx context.Context, model string, groupID int64) bool

// tkPricedServingGateCanaryModel 是降级探测用的常驻已定价 canary。它必须在任何健康的
// pricing 系统里恒解析出价：gemini-2.5-pro 常驻在【内嵌 litellm 镜像】
// (resources/model-pricing/model_prices_and_context_window.json)，随二进制打包、不依赖远程
// 下载或 overlay 是否加载成功；故「连它都未定价」唯一可能是 pricing 源整体被灌入坏数据/空集
// （远程下载失败或 overlay 热推坏数据）——此时闸 fail-open 放行，不把定价文件 glitch 放大成整
// 服务 404。注：gemini 一口价 fallback 已删（commit C「查不到就告警」），canary 的健壮性来自
// 内嵌镜像、不再来自 getFallbackPricing（修正旧注释的失实说法）。
const tkPricedServingGateCanaryModel = "gemini-2.5-pro"

// tkPricingSystemDegraded 报告 pricing 神谕是否整体降级（而非「某个模型恰好未定价」）。
// 判据：常驻已定价 canary 都解析为未定价 ⇒ 源加载失败/降级。健康系统永远 false（canary
// 常驻内嵌 litellm 镜像）。降级时闸必须 fail-open（与 billing 降级 fail-open $0 同向），否则一次
// 定价文件 glitch 会让闸 404 掉 100% 启用平台流量。
func tkPricingSystemDegraded(resolve tkBillingPricingResolver) bool {
	if resolve == nil {
		// resolver 未注入：交给上层 nil 检查 fail-open；这里不单独判降级。
		return false
	}
	_, err := resolve(tkPricedServingGateCanaryModel)
	return errors.Is(err, ErrModelPricingUnavailable)
}

// tkPricedServingGateRejected 是闸内部的判定结果：true = 应拒绝（未定价且平台已启用且系统健康）。
// 拆成独立纯函数便于单测断言「开/关 × 定价/未定价 × 降级」矩阵，不依赖 gin/notifier。
//
// 短路顺序（性能 + 正确性）：
//  1. resolver/setting 未注入 → false（放行，零开销）；
//  2. setting 未启用该平台 → false（放行，不调 billing）；
//  3. billing 基础价神谕解析出价（GetModelPricing 不返 ErrModelPricingUnavailable）→ 放行；
//  4. 基础价缺失时再查【渠道价】：该 group 对该 model 配了 channel_model_pricing
//     （Source==PricingSourceChannel，billing 计费路径 resolveChannelPricing 会按它非零计费）→ 放行；
//  5. 两个价源都未定价时再探降级：pricing 系统整体降级 → fail-open 放行；
//  6. 否则拒绝（系统健康、平台已启用、该模型基础价 + 渠道价都真未定价）。
//
// 闸用 billing 自己的【两个】价源（GetModelPricing 基础价 + resolver 渠道价），与 billing
// 计费路径（CalculateCost ← GetModelPricing；resolveChannelPricing ← resolver.Resolve）一一对应，
// 而非 catalog 成员影子谓词——这是 R3「闸 ⟺ billing 构造性成立」的根：billing 用这两个源决定
// 记不记 $0，闸用同样两个源、同一个键，永不漂移（含 fallback family + 全维度字段 + 渠道价）。
// 早先只问 GetModelPricing 漏了渠道价 → 误拒「渠道有价、基础价缺」的可计费模型（复审 BLOCKER B1）。
// 详见文件头根因说明。
func tkPricedServingGateRejected(
	ctx context.Context,
	resolve tkBillingPricingResolver,
	channelProbe tkChannelPricingProbe,
	setting *SettingService,
	billingModel, platform string,
	groupID int64,
) bool {
	if setting == nil || resolve == nil {
		// 依赖未注入（降级/测试接线）→ 永不拒绝。闸是叠加的减法，缺依赖必须 fail-open，
		// 绝不因接线问题误拒真实流量。
		return false
	}
	if !setting.IsPricedServingGateEnabled(ctx, platform) {
		return false
	}
	if _, err := resolve(billingModel); !errors.Is(err, ErrModelPricingUnavailable) {
		// 基础价有（或非「不可用」错误，按健康放行——只有明确的 unavailable 才是漏血信号）。
		return false
	}
	// 基础价缺失：再查渠道价。billing 计费路径 resolveChannelPricing 对 per-request/image 渠道价、
	// 以及覆盖在缺失基础价之上的 token 渠道价仍按非零计费；闸必须同样认它，否则误拒可计费模型（B1）。
	if channelProbe != nil && groupID != 0 && channelProbe(ctx, billingModel, groupID) {
		return false
	}
	// 基础价 + 渠道价都解析为未定价：先排除「pricing 系统整体降级」——降级时 fail-open 放行，
	// 不把定价源 glitch 放大成整服务 404（SHOULD-FIX，与 billing 降级 fail-open 同向）。
	if tkPricingSystemDegraded(resolve) {
		return false
	}
	return true
}

// tkCheckPricedServingGate 是各路线注入的统一闸点。返回 true = 放行（继续转发）；
// false = 已拒绝（已写 404 响应 + 触发告警），调用方必须立即 return 不再转发。
//
// requestedModel 用于告警样例展示（客户端原始模型名），billingModel 是判定键 +
// 拒绝文案里点名的模型（**必须是 billing 将记账的确切键**：native gemini/anthropic 是
// originalModel，openai native 是 mapped billingModel）。platform 是 account.Platform。
// wireProtocol 决定 404 body 形态（**按调用方实际讲的协议、非 account.Platform**，破 D1 的
// BLOCKER4 修法）：Forward(anthropic ingress)=anthropic、ForwardNative=gemini、
// ForwardAs{ChatCompletions,Responses}=openai。
//
// nil-safe：resolve/setting/c 任一为 nil 都安全放行（见 tkPricedServingGateRejected）。
func tkCheckPricedServingGate(
	ctx context.Context,
	resolve tkBillingPricingResolver,
	channelProbe tkChannelPricingProbe,
	setting *SettingService,
	notifier PricingMissingNotifier,
	c *gin.Context,
	wireProtocol tkGateWireProtocol,
	platform, billingModel, requestedModel string,
) bool {
	// groupID 用于渠道价探测（B1）：从 gin context 经 api_key 取，与下面告警取 group 同源。
	groupID := tkGateGroupID(c)
	if !tkPricedServingGateRejected(ctx, resolve, channelProbe, setting, billingModel, platform, groupID) {
		return true
	}
	tkWritePricedServingGateRejection(c, wireProtocol)
	tkLogAndNotifyPricedServingGateRejection(c, notifier, platform, billingModel, requestedModel)
	return false
}

// tkGateGroupID 从 gin context 取请求所属 group 的 ID（经 api_key），供闸的渠道价探测使用。
// 取不到（无 c / 无 api_key / 无 group）返 0 —— 探测随即跳过，退化为仅基础价判定（安全方向）。
func tkGateGroupID(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	if g := apiKeyGroup(getAPIKeyFromContext(c)); g != nil {
		return g.ID
	}
	return 0
}

// tkWritePricedServingGateRejection 按**客户端 wire 协议**字节对齐写 404 拒绝 body。HTTP
// 状态统一 404，子码 model_not_priced 只在能放下的协议进 body（OpenAI/NewAPI 有 code 字段；
// Anthropic 无 code 字段，子码只走日志；Gemini 是 numeric-code 形）。
//
// 关键（BLOCKER4）：形按 wireProtocol 选，**不**按 account.Platform——一个 gemini 账号可能在
// 跑 Anthropic /v1/messages ingress（Forward），客户端是 Anthropic SDK 读 error.type，必须回
// Anthropic 形；反之 anthropic 账号跑 openai 协议（ForwardAs*）必须回 OpenAI 形。
func tkWritePricedServingGateRejection(c *gin.Context, wireProtocol tkGateWireProtocol) {
	if c == nil {
		return
	}
	MarkResponseCommitted(c)
	switch wireProtocol {
	case tkGateWireAnthropic:
		// Anthropic 形：{type:error, error:{type:not_found_error, message}}（无 code 字段）。
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "not_found_error",
				"message": tkPricedServingGateMessage,
			},
		})
	case tkGateWireGemini:
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

// tkGateWireProtocol 是拒绝 body 形态的选择维度：**客户端实际讲的协议**，不是 account.Platform。
// 同一个账号平台可能服务多种 ingress 协议（gemini 账号跑 Anthropic /v1/messages、anthropic
// 账号跑 openai /v1/chat/completions），404 信封必须匹配客户端 SDK 在读的字段（BLOCKER4）。
type tkGateWireProtocol int

const (
	// tkGateWireOpenAI = OpenAI 兼容信封（含 newapi compat 系）。零值默认。
	tkGateWireOpenAI tkGateWireProtocol = iota
	// tkGateWireAnthropic = Anthropic /v1/messages 信封（error.type=not_found_error）。
	tkGateWireAnthropic
	// tkGateWireGemini = Google generativelanguage 信封（error.status=NOT_FOUND）。
	tkGateWireGemini
)

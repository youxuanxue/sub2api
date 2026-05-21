package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// TempUnscheduler 用于 HandleFailoverError 中同账号重试耗尽后的临时封禁。
// GatewayService 隐式实现此接口。
type TempUnscheduler interface {
	TempUnscheduleRetryableError(ctx context.Context, accountID int64, failoverErr *service.UpstreamFailoverError)
}

// FailoverAction 表示 failover 错误处理后的下一步动作
type FailoverAction int

const (
	// FailoverContinue 继续循环（同账号重试或切换账号，调用方统一 continue）
	FailoverContinue FailoverAction = iota
	// FailoverExhausted 切换次数耗尽（调用方应返回错误响应）
	FailoverExhausted
	// FailoverCanceled context 已取消（调用方应直接 return）
	FailoverCanceled
)

const (
	// sameAccountRetryDelay 同账号重试间隔
	sameAccountRetryDelay = 500 * time.Millisecond
	// singleAccountBackoffDelay 单账号分组 503 退避重试固定延时。
	// Service 层在 SingleAccountRetry 模式下已做充分原地重试（最多 3 次、总等待 30s），
	// Handler 层只需短暂间隔后重新进入 Service 层即可。
	singleAccountBackoffDelay = 2 * time.Second
)

// FailoverState 跨循环迭代共享的 failover 状态
type FailoverState struct {
	SwitchCount           int
	MaxSwitches           int
	FailedAccountIDs      map[int64]struct{}
	SameAccountRetryCount map[int64]int
	LastFailoverErr       *service.UpstreamFailoverError
	ForceCacheBilling     bool
	hasBoundSession       bool
}

// NewFailoverState 创建 failover 状态
func NewFailoverState(maxSwitches int, hasBoundSession bool) *FailoverState {
	return &FailoverState{
		MaxSwitches:           maxSwitches,
		FailedAccountIDs:      make(map[int64]struct{}),
		SameAccountRetryCount: make(map[int64]int),
		hasBoundSession:       hasBoundSession,
	}
}

// HandleFailoverError 处理 UpstreamFailoverError，返回下一步动作。
// 包含：缓存计费判断、同账号重试、临时封禁、切换计数、Antigravity 延时。
//
// sameAccountRetryLimit 是本账号的同账号 retry 上限。生产路径应传
// account.GetPoolModeRetryCount()（pool_mode 账号读 credentials.pool_mode_retry_count，
// 非 pool_mode 返回 defaultPoolModeRetryCount=1）。传 0 表示"不允许同账号 retry，
// 立即 failover"——这是 UI hint 承诺的语义（i18n: "0 = 不原地重试"）。负数同样
// 视为 0；不做隐式升值，避免运维显式禁用 retry 时被悄悄改成 1 次。
func (s *FailoverState) HandleFailoverError(
	ctx context.Context,
	gatewayService TempUnscheduler,
	accountID int64,
	platform string,
	sameAccountRetryLimit int,
	failoverErr *service.UpstreamFailoverError,
) FailoverAction {
	if sameAccountRetryLimit < 0 {
		sameAccountRetryLimit = 0
	}
	s.LastFailoverErr = failoverErr

	// 缓存计费判断
	if needForceCacheBilling(s.hasBoundSession, failoverErr) {
		s.ForceCacheBilling = true
	}

	// TK fail-fast: 上游 403 且响应体不是结构化错误 JSON（任何 platform shape）→
	// 视为"请求级"失败（典型场景：claude-cli 大 body 被 Cloudflare/WAF 拦截，
	// 上游边缘网关直接 403 + 空 body 或 HTML 错误页），切账号无用，直接
	// FailoverExhausted。
	//
	// 判定 looksLikeStructuredErrorJSON 兼容三家 shape：
	//   - anthropic: {"type":"error","error":{...}}
	//   - openai:    {"error":{"message":"...","type":"...","code":"..."}}
	//   - gemini:    {"error":{"code":403,"message":"...","status":"..."}}
	// 任何 platform 的结构化 JSON 错误都视为账号级问题（key 失效 / 账号被封 /
	// scope 越界），走原 failover 切其他账号。只有非 JSON / 空 body 才视为
	// 请求级 cloudflare 拦截。
	//
	// 不调用 TempUnscheduleRetryableError（403 非 transient），不递增 SwitchCount
	// （避免日志噪声 + 给 us1 这种单 schedulable 账号场景节省 retry 预算）。
	// 仍把账号加入 FailedAccountIDs，防止上层 retry loop 立刻再选回同账号。
	// 详见排查记录：account_id=1 (cc-am-or-ec2-5-1-b) 上 10 次 403 全部 ResponseBody 空。
	if failoverErr.StatusCode == http.StatusForbidden && !looksLikeStructuredErrorJSON(failoverErr.ResponseBody) {
		logger.FromContext(ctx).Warn("gateway.failover_forbidden_fail_fast",
			zap.Int64("account_id", accountID),
			zap.String("platform", platform),
			zap.Int("upstream_status", failoverErr.StatusCode),
			zap.Int("response_body_bytes", len(failoverErr.ResponseBody)),
		)
		s.FailedAccountIDs[accountID] = struct{}{}
		return FailoverExhausted
	}

	// 同账号重试：对 RetryableOnSameAccount 的临时性错误，先在同一账号上重试
	if failoverErr.RetryableOnSameAccount && s.SameAccountRetryCount[accountID] < sameAccountRetryLimit {
		s.SameAccountRetryCount[accountID]++
		logger.FromContext(ctx).Warn("gateway.failover_same_account_retry",
			zap.Int64("account_id", accountID),
			zap.Int("upstream_status", failoverErr.StatusCode),
			zap.Int("same_account_retry_count", s.SameAccountRetryCount[accountID]),
			zap.Int("same_account_retry_max", sameAccountRetryLimit),
		)
		if !sleepWithContext(ctx, sameAccountRetryDelay) {
			return FailoverCanceled
		}
		return FailoverContinue
	}

	// 同账号重试用尽，执行临时封禁
	if failoverErr.RetryableOnSameAccount {
		gatewayService.TempUnscheduleRetryableError(ctx, accountID, failoverErr)
	}

	// 加入失败列表
	s.FailedAccountIDs[accountID] = struct{}{}

	// 检查是否耗尽
	if s.SwitchCount >= s.MaxSwitches {
		return FailoverExhausted
	}

	// 递增切换计数
	s.SwitchCount++
	logger.FromContext(ctx).Warn("gateway.failover_switch_account",
		zap.Int64("account_id", accountID),
		zap.Int("upstream_status", failoverErr.StatusCode),
		zap.Int("switch_count", s.SwitchCount),
		zap.Int("max_switches", s.MaxSwitches),
	)

	// Antigravity 平台换号线性递增延时
	if platform == service.PlatformAntigravity {
		delay := time.Duration(s.SwitchCount-1) * time.Second
		if !sleepWithContext(ctx, delay) {
			return FailoverCanceled
		}
	}

	return FailoverContinue
}

// HandleSelectionExhausted 处理选号失败（所有候选账号都在排除列表中）时的退避重试决策。
// 针对 Antigravity 单账号分组的 503 (MODEL_CAPACITY_EXHAUSTED) 场景：
// 清除排除列表、等待退避后重新选号。
//
// 返回 FailoverContinue 时，调用方应设置 SingleAccountRetry context 并 continue。
// 返回 FailoverExhausted 时，调用方应返回错误响应。
// 返回 FailoverCanceled 时，调用方应直接 return。
func (s *FailoverState) HandleSelectionExhausted(ctx context.Context) FailoverAction {
	if s.LastFailoverErr != nil &&
		s.LastFailoverErr.StatusCode == http.StatusServiceUnavailable &&
		s.SwitchCount <= s.MaxSwitches {

		logger.FromContext(ctx).Warn("gateway.failover_single_account_backoff",
			zap.Duration("backoff_delay", singleAccountBackoffDelay),
			zap.Int("switch_count", s.SwitchCount),
			zap.Int("max_switches", s.MaxSwitches),
		)
		if !sleepWithContext(ctx, singleAccountBackoffDelay) {
			return FailoverCanceled
		}
		logger.FromContext(ctx).Warn("gateway.failover_single_account_retry",
			zap.Int("switch_count", s.SwitchCount),
			zap.Int("max_switches", s.MaxSwitches),
		)
		s.FailedAccountIDs = make(map[int64]struct{})
		return FailoverContinue
	}
	return FailoverExhausted
}

// needForceCacheBilling 判断 failover 时是否需要强制缓存计费。
// 粘性会话切换账号、或上游明确标记时，将 input_tokens 转为 cache_read 计费。
func needForceCacheBilling(hasBoundSession bool, failoverErr *service.UpstreamFailoverError) bool {
	return hasBoundSession || (failoverErr != nil && failoverErr.ForceCacheBilling)
}

// sleepWithContext 等待指定时长，返回 false 表示 context 已取消。
func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// looksLikeStructuredErrorJSON 判断上游 ResponseBody 是否为某种 platform 的结构化
// 错误 JSON（anthropic / openai / gemini 任一）。
//
// 兼容的错误 shape:
//
//   - anthropic: {"type":"error","error":{"type":"...","message":"..."}}
//   - openai:    {"error":{"message":"...","type":"...","code":"..."}}
//   - gemini:    {"error":{"code":403,"message":"...","status":"..."}}
//
// 命中策略：有效 JSON 且顶层有 `error` 字段（object）即视为结构化错误，
// 走原 failover 切账号路径；anthropic 顶层 `"type":"error"` 不再强制要求。
//
// 任何其他形态（空 body、HTML、Cloudflare 错误页、纯文本、合法 JSON 但无 error 字段）
// 都视为"非结构化错误"，在 403 场景下触发 fail-fast。
func looksLikeStructuredErrorJSON(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if !json.Valid(body) {
		return false
	}
	return gjson.GetBytes(body, "error").IsObject()
}

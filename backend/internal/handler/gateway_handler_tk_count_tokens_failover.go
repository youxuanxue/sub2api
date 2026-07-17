package handler

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// forwardCountTokensWithFailover 在 count_tokens 路径上套用与 /v1/messages 一致的
// 账号 failover loop。
//
// 背景（现场：edge us1 / prod cc-us1 pool_mode）：
//   - 旧实现是「选一个账号 → ForwardCountTokens 单发 → 任何上游错误直接回客户端」，
//     无换号。结果 acct1 的一次上游 529 直接漏给客户端，即便 acct4 空闲可调度。
//   - ForwardCountTokens / passthrough 现对可 failover 状态码（401/403/429/529/5xx）
//     返回 *service.UpstreamFailoverError 且不写响应体；本 loop 负责换号 / 池内轮换 /
//     耗尽后回写。
//
// 复用 FailoverState.HandleFailoverError，因此 pool_mode stub 的同账号重试
// （= 池内轮换到下个上游成员）语义自动生效，无需在 count_tokens 另写一套。
//
// body 快照/还原：ForwardCountTokens 会就地改写 parsedReq 的 body 与 Model
// （strip / OAuth mimic / 模型映射）。换号重试前必须还原到原始 body+model，避免
// 在已变换的 body 上二次变换（尤其 OAuth↔APIKey 账号混排时变换规则不同）。
func (h *GatewayHandler) forwardCountTokensWithFailover(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	sessionHash string,
	parsedReq *service.ParsedRequest,
) {
	// 调用方（CountTokens handler）已保证 parsedReq 非空且 body 已解析；这里直接用。
	fs := NewFailoverState(h.maxAccountSwitches, sessionHash != "")

	// 原始 body/model 快照，用于换号重试前还原（见上方说明）。
	originalBody := append([]byte(nil), parsedReq.Body.Bytes()...)
	originalModel := parsedReq.Model

	first := true
	for {
		if !first {
			// 还原到原始请求，避免重复变换。
			if err := parsedReq.ReplaceBody(originalBody); err != nil {
				reqLog.Error("gateway.count_tokens_restore_body_failed", zap.Error(err))
				h.errorResponse(c, http.StatusInternalServerError, "api_error", "Service temporarily unavailable")
				return
			}
			parsedReq.Model = originalModel
		}
		first = false

		account, err := h.gatewayService.SelectAccountForModelWithExclusions(
			c.Request.Context(), apiKey.GroupID, sessionHash, parsedReq.Model, fs.FailedAccountIDs,
		)
		if err != nil {
			// 选号落空。若此前已发生过 failover 错误，按最后一次上游错误回客户端，
			// 保留真实状态码（429/529/...）；否则空池快速失败 429（#575 语义），
			// 其余调度错误（DB 故障等）保持 503。
			if fs.LastFailoverErr != nil {
				h.gatewayService.WriteCountTokensFailoverError(c, fs.LastFailoverErr)
				return
			}
			reqLog.Warn("gateway.count_tokens_select_account_failed", zap.Error(err))
			markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
			tkStatus, tkType, tkMsg := tkSelectFailureStatusMessage(c, err, parsedReq.Model)
			h.errorResponse(c, tkStatus, tkType, tkMsg)
			return
		}
		setOpsSelectedAccount(c, account.ID, account.Platform)

		err = h.gatewayService.ForwardCountTokens(c.Request.Context(), c, account, parsedReq)
		if err == nil {
			return // 成功（响应已在 ForwardCountTokens 中写出）
		}

		var failoverErr *service.UpstreamFailoverError
		if errors.As(err, &failoverErr) {
			action := fs.HandleFailoverError(
				c.Request.Context(), h.gatewayService,
				account.ID, account.Platform, account.GetPoolModeRetryCount(), failoverErr,
			)
			switch action {
			case FailoverContinue:
				continue
			case FailoverExhausted:
				h.gatewayService.WriteCountTokensFailoverError(c, fs.LastFailoverErr)
				return
			case FailoverCanceled:
				return
			}
		}

		// 非 failover 错误：ForwardCountTokens 已写客户端响应，直接结束。
		reqLog.Error("gateway.count_tokens_forward_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		return
	}
}

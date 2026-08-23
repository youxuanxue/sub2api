package handler

import (
	"context"
	"sync"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// openAIWSTurnPricing 持有 WebSocket 连接内「当前 turn」的计费定价时刻。
// 由 BeforeTurn 在每个 turn 开始时冻结，AfterTurn 的用量提交读取它；turn 在
// 连接内串行推进，互斥锁只为跨用量提交 goroutine 的读取安全。
//
// ws_v2 passthrough ingress 没有 BeforeTurn，因此本值会保持零；AfterTurn 必须
// 以 TurnStarted 已记录的所属 turn 开始时刻为回退，而不是用建连或记录时刻。
// 这样每个 passthrough turn 都按自己的开始时刻计价，但不改变其仅在建连时执行
// 准入门、没有 turn 级利润复核的既有行为。
type openAIWSTurnPricing struct {
	mu sync.Mutex
	at time.Time
}

func (p *openAIWSTurnPricing) freeze(at time.Time) {
	p.mu.Lock()
	p.at = at
	p.mu.Unlock()
}

func (p *openAIWSTurnPricing) currentOr(fallback time.Time) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.at.IsZero() {
		return p.at
	}
	return fallback
}

// tkWSResponsesTurnHold owns per-connection WS turn balance reservations.
type tkWSResponsesTurnHold struct {
	h      *OpenAIGatewayHandler
	c      *gin.Context
	apiKey *service.APIKey
	handle *tkHoldHandle
	seq    int
}

func (h *OpenAIGatewayHandler) tkNewWSResponsesTurnHold(c *gin.Context, apiKey *service.APIKey) *tkWSResponsesTurnHold {
	return &tkWSResponsesTurnHold{h: h, c: c, apiKey: apiKey}
}

func (w *tkWSResponsesTurnHold) ReleaseUnlessSettling() {
	if w == nil || w.handle == nil {
		return
	}
	w.handle.ReleaseUnlessSettling()
}

func (w *tkWSResponsesTurnHold) HasHold() bool {
	return w != nil && w.handle != nil
}

func (w *tkWSResponsesTurnHold) Reserve(model string, payload []byte) error {
	if w == nil {
		return nil
	}
	w.seq++
	turnHold, holdReject := w.h.tkApplyWSTurnHold(w.c, w.apiKey, model, payload, w.seq)
	if holdReject {
		return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, tkInsufficientBalanceForHoldMsg, nil)
	}
	w.handle = turnHold
	return nil
}

func (w *tkWSResponsesTurnHold) HandOffForTurn() string {
	if w == nil || w.handle == nil {
		return ""
	}
	requestID := w.handle.HandOffToSettlement()
	w.handle = nil
	return requestID
}

type tkWSBeforeTurnInput struct {
	Turn                  int
	CyberBlockedThisConn  bool
	APIKey                *service.APIKey
	Account               *service.Account
	AccountMaxConcurrency int
	Subject               middleware2.AuthSubject
	ReqLog                *zap.Logger
	TurnPricing           *openAIWSTurnPricing
	ReleaseTurnSlots      func()
	CurrentUserRelease    *func()
	CurrentAccountRelease *func()
}

func (h *OpenAIGatewayHandler) tkWSBeforeTurn(
	ctx context.Context,
	in tkWSBeforeTurnInput,
) error {
	if in.CyberBlockedThisConn {
		return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, cyberSessionBlockedClientMsg, nil)
	}
	turnCtx, turnAt := h.gatewayService.WithOpenAITurnPricingContext(ctx, in.APIKey.GroupID)
	if _, vetoed, reason := h.gatewayService.ProfitControlVetoLatest(turnCtx, in.Account); vetoed {
		in.ReqLog.Info("openai.websocket_turn_profit_vetoed",
			zap.Int("turn", in.Turn),
			zap.Int64("account_id", in.Account.ID),
			zap.String("reason", reason))
		return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "account is no longer eligible for this connection, please reconnect", nil)
	}
	in.TurnPricing.freeze(turnAt)
	if in.Turn == 1 {
		return nil
	}
	in.ReleaseTurnSlots()
	userReleaseFunc, userAcquired, err := h.concurrencyHelper.TryAcquireUserSlotForAPIKey(ctx, in.Subject.UserID, in.Subject.Concurrency, in.APIKey.ID)
	if err != nil {
		return service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to acquire user concurrency slot", err)
	}
	if !userAcquired {
		return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "too many concurrent requests, please retry later", nil)
	}
	accountReleaseFunc, accountAcquired, err := h.concurrencyHelper.TryAcquireAccountSlot(ctx, in.Account.ID, in.AccountMaxConcurrency)
	if err != nil {
		if userReleaseFunc != nil {
			userReleaseFunc()
		}
		return service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to acquire account concurrency slot", err)
	}
	if !accountAcquired {
		if userReleaseFunc != nil {
			userReleaseFunc()
		}
		return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "account is busy, please retry later", nil)
	}
	*in.CurrentUserRelease = wrapReleaseOnDone(ctx, userReleaseFunc)
	*in.CurrentAccountRelease = wrapReleaseOnDone(ctx, accountReleaseFunc)
	return nil
}

type tkWSAfterTurnUsageInput struct {
	Ctx                context.Context
	C                  *gin.Context
	ReqLog             *zap.Logger
	APIKey             *service.APIKey
	Account            *service.Account
	Subscription       *service.UserSubscription
	TurnHold           *tkWSResponsesTurnHold
	TurnPricing        *openAIWSTurnPricing
	Turn               int
	TurnStart          time.Time
	Result             *service.OpenAIForwardResult
	TurnMapping        service.ChannelMappingResult
	TurnRequestedModel string
	TurnUpstreamModel  string
	RequestPayloadHash string
	UserAgent          string
	ClientIP           string
}

func (h *OpenAIGatewayHandler) tkWSAfterTurnSubmitUsage(in tkWSAfterTurnUsageInput) {
	if in.Result == nil {
		return
	}
	in.Result.BillingModel = openAIWSTurnBillingModel(in.Result, in.TurnMapping, in.TurnRequestedModel, in.TurnUpstreamModel)
	in.ReqLog.Debug("openai.websocket_turn_billing",
		zap.Int("turn", in.Turn),
		zap.String("turn_requested_model", in.TurnRequestedModel),
		zap.String("turn_upstream_model", in.TurnUpstreamModel),
		zap.String("billing_model", in.Result.BillingModel),
	)
	if in.Account.Type == service.AccountTypeOAuth && !in.Account.IsShadow() {
		h.gatewayService.UpdateCodexUsageSnapshotFromHeaders(in.Ctx, in.Account.ID, in.Result.ResponseHeaders)
	}
	scheduleModel := in.TurnUpstreamModel
	if scheduleModel == "" {
		scheduleModel = in.TurnRequestedModel
	}
	h.gatewayService.ReportOpenAIAccountScheduleResult(in.Account, scheduleModel, openAIForwardSucceededForScheduling(in.Result), in.Result.FirstTokenMs)
	tkHoldRequestID := in.TurnHold.HandOffForTurn()
	quotaPlatform := service.QuotaPlatform(in.C.Request.Context(), in.APIKey)
	sessionID := service.ExtractClientSessionID(in.C)
	turnRecordPricingAt := in.TurnPricing.currentOr(in.TurnStart)
	cyberBlocked := service.GetOpsCyberPolicy(in.C) != nil
	turnUsageFields := in.TurnMapping.ToUsageFields(in.TurnRequestedModel, in.TurnUpstreamModel)
	h.submitOpenAIUsageRecordTask(in.Ctx, in.Result, func(taskCtx context.Context) {
		if err := h.gatewayService.RecordUsage(taskCtx, &service.OpenAIRecordUsageInput{
			Result:             in.Result,
			APIKey:             in.APIKey,
			User:               in.APIKey.User,
			Account:            in.Account,
			Subscription:       in.Subscription,
			InboundEndpoint:    GetInboundEndpoint(in.C),
			UpstreamEndpoint:   resolveOpenAIUpstreamEndpoint(in.C, in.Account, in.Result),
			UserAgent:          in.UserAgent,
			IPAddress:          in.ClientIP,
			RequestPayloadHash: in.RequestPayloadHash,
			APIKeyService:      h.apiKeyService,
			TkHoldRequestID:    tkHoldRequestID,
			QuotaPlatform:      quotaPlatform,
			SessionID:          sessionID,
			ChannelUsageFields: turnUsageFields,
			PricingAt:          turnRecordPricingAt,
			CyberBlocked:       cyberBlocked,
		}); err != nil {
			in.ReqLog.Error("openai.websocket_record_usage_failed",
				zap.Int64("account_id", in.Account.ID),
				zap.String("request_id", in.Result.RequestID),
				zap.Error(err),
			)
		}
	})
}

package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// tkMessagesGeminiPlatform owns the Gemini-platform /v1/messages failover loop
// (Antigravity ForwardGemini + GeminiMessagesCompatService.Forward with
// TKGeminiDispatchGroupContextKey). Called thinly from Messages when
// platform == PlatformGemini; always completes the request (writes response
// or error) and does not fall through to the Anthropic path.
func (h *GatewayHandler) tkMessagesGeminiPlatform(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	sessionKey string,
	hasBoundSession bool,
	reqModel string,
	reqStream bool,
	body []byte,
	parsedReq *service.ParsedRequest,
	isClaudeCodeClient bool,
	streamStarted bool,
	platform string,
	pricingAt time.Time,
	channelMapping service.ChannelMappingResult,
) {
	fs := NewFailoverState(h.maxAccountSwitchesGemini, hasBoundSession)

	// 单账号分组提前设置 SingleAccountRetry 标记，让 Service 层首次 503 就不设模型限流标记。
	// 避免单账号分组收到 503 (MODEL_CAPACITY_EXHAUSTED) 时设 29s 限流，导致后续请求连续快速失败。
	if h.gatewayService.IsSingleAntigravityAccountGroup(c.Request.Context(), apiKey.GroupID) {
		ctx := service.WithSingleAccountRetry(c.Request.Context(), true, h.metadataBridgeEnabled())
		c.Request = c.Request.WithContext(ctx)
	}

	for {
		routingStart := time.Now()
		selection, err := h.gatewayService.SelectAccountWithLoadAwareness(c.Request.Context(), apiKey.GroupID, sessionKey, reqModel, fs.FailedAccountIDs, "", int64(0)) // Gemini 不使用会话限制
		if err != nil {
			if len(fs.FailedAccountIDs) == 0 {
				if h.tkWriteDeprecatedAnthropicModelIfApplicable(c, err, reqModel, reqLog) {
					return
				}
				if h.tkWriteUnsupportedModelIfApplicable(c, err, reqModel, streamStarted, reqLog) {
					return
				}
				markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				reqLog.Warn("gateway.select_account_no_available",
					zap.String("model", reqModel),
					zap.Int64p("group_id", apiKey.GroupID),
					zap.String("platform", platform),
					zap.Error(err),
				)
				h.handleStreamingAwareError(c, tkNoAvailableAccounts(c), "api_error", "No available accounts: "+err.Error(), streamStarted)
				return
			}
			action := fs.HandleSelectionExhausted(c.Request.Context(), errors.Is(err, service.ErrThinPoolAllExcluded))
			switch action {
			case FailoverContinue:
				ctx := service.WithSingleAccountRetry(c.Request.Context(), true, h.metadataBridgeEnabled())
				c.Request = c.Request.WithContext(ctx)
				continue
			case FailoverCanceled:
				failoverClientGone(c)
				return
			default: // FailoverExhausted
				if fs.LastFailoverErr != nil {
					h.handleFailoverExhausted(c, fs.LastFailoverErr, service.PlatformGemini, streamStarted)
				} else {
					h.handleFailoverExhaustedSimple(c, 502, streamStarted)
				}
				return
			}
		}
		account := selection.Account
		setOpsSelectedAccountFrom(c, account)

		// 检查请求拦截（预热请求、SUGGESTION MODE等）
		if account.IsInterceptWarmupEnabled() {
			interceptType := detectInterceptType(body, reqModel, parsedReq.MaxTokens, isClaudeCodeClient)
			if interceptType != InterceptTypeNone {
				if selection.Acquired && selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				if reqStream {
					sendMockInterceptStream(c, reqModel, interceptType, account)
				} else {
					sendMockInterceptResponse(c, reqModel, interceptType, account)
				}
				return
			}
		}

		// 3. 获取账号并发槽位
		accountReleaseFunc := selection.ReleaseFunc
		if !selection.Acquired {
			if selection.WaitPlan == nil {
				markOpsRoutingCapacityLimited(c)
				reqLog.Warn("gateway.select_account_no_slot_no_wait_plan",
					zap.Int64("account_id", account.ID),
					zap.String("model", reqModel),
					zap.String("platform", platform),
				)
				h.handleStreamingAwareError(c, tkNoAvailableAccounts(c), "api_error", "No available accounts", streamStarted)
				return
			}
			accountWaitCounted := false
			canWait, err := h.concurrencyHelper.IncrementAccountWaitCount(c.Request.Context(), account.ID, selection.WaitPlan.MaxWaiting)
			if err != nil {
				reqLog.Warn("gateway.account_wait_counter_increment_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			} else if !canWait {
				reqLog.Info("gateway.account_wait_queue_full",
					zap.Int64("account_id", account.ID),
					zap.Int("max_waiting", selection.WaitPlan.MaxWaiting),
				)
				h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", streamStarted)
				return
			}
			if err == nil && canWait {
				accountWaitCounted = true
			}
			releaseWait := func() {
				if accountWaitCounted {
					h.concurrencyHelper.DecrementAccountWaitCount(c.Request.Context(), account.ID)
					accountWaitCounted = false
				}
			}

			accountReleaseFunc, err = h.concurrencyHelper.AcquireAccountSlotWithWaitTimeout(
				c,
				account.ID,
				selection.WaitPlan.MaxConcurrency,
				selection.WaitPlan.Timeout,
				reqStream,
				&streamStarted,
			)
			if err != nil {
				reqLog.Warn("gateway.account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
				releaseWait()
				h.handleConcurrencyError(c, err, "account", streamStarted)
				return
			}
			// Slot acquired: no longer waiting in queue.
			releaseWait()
		}
		// 终检与准入后绑定使用选号结果携带的门（见 responses 同名注释）。
		admissionCtx := service.ContextWithSelectionProfitGate(c.Request.Context(), selection)
		latest, vetoed, reason := h.gatewayService.GatewayProfitControlVetoLatest(admissionCtx, account)
		if vetoed {
			if accountReleaseFunc != nil {
				accountReleaseFunc()
			}
			reqLog.Debug("gateway.account_slot_profit_vetoed", zap.Int64("account_id", account.ID), zap.String("reason", reason))
			if fs.RecordProfitVeto(account.ID) == FailoverExhausted {
				reqLog.Warn("gateway.profit_veto_attempts_exhausted", zap.Int("profit_veto_count", fs.ProfitVetoCount()))
				markOpsRoutingCapacityLimited(c)
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", profitVetoExhaustedMessage, streamStarted)
				return
			}
			continue
		}
		account = latest
		selection.Account = latest
		// 等待路径保持既有 eager 绑定（无门时 helper 直接绑定）；调度器已
		// 抢槽的直达路径无门时由选号内部绑定，这里只在门下补准入后绑定。
		if selection.ProfitGateActive() || !selection.Acquired {
			if err := h.gatewayService.BindStickySessionAfterProfitAdmission(admissionCtx, apiKey.GroupID, sessionKey, account.ID); err != nil {
				reqLog.Warn("gateway.bind_sticky_session_after_profit_admission_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			}
		}
		// 账号槽位/等待计数需要在超时或断开时安全回收
		accountReleaseFunc = wrapReleaseOnDone(c.Request.Context(), accountReleaseFunc)

		tkRecordRoutingLatency(c, routingStart)

		// 转发请求 - 根据账号平台分流
		var result *service.ForwardResult
		requestCtx := c.Request.Context()
		if fs.SwitchCount > 0 {
			requestCtx = service.WithAccountSwitchCount(requestCtx, fs.SwitchCount, h.metadataBridgeEnabled())
		}
		// 记录 Forward 前已写入字节数，Forward 后若增加则说明 SSE 内容已发，禁止 failover
		writerSizeBeforeForward := c.Writer.Size()
		forwardStart := time.Now()
		if account.Platform == service.PlatformAntigravity {
			result, err = h.antigravityGatewayService.ForwardGemini(
				requestCtx,
				c,
				account,
				reqModel,
				"generateContent",
				reqStream,
				body,
				hasBoundSession,
				service.WithForwardGeminiSession(derefGroupID(apiKey.GroupID), sessionKey),
			)
		} else {
			// TK: 让 GeminiMessagesCompatService.Forward 能拿到 *Group 做
			// 分组级 Claude→Gemini 映射 (gin_gemini_dispatch_tk.go)。
			c.Set(service.TKGeminiDispatchGroupContextKey, apiKey.Group)
			result, err = h.geminiCompatService.Forward(requestCtx, c, account, body)
		}
		tkRecordForwardResponseTail(c, forwardStart)
		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}
		if err != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				// 流式内容已写入客户端，无法撤销，禁止 failover 以防止流拼接腐化
				if c.Writer.Size() != writerSizeBeforeForward {
					h.handleFailoverExhausted(c, failoverErr, service.PlatformGemini, true)
					return
				}
				action := fs.HandleFailoverError(c.Request.Context(), h.gatewayService, account.ID, account.Platform, account.GetPoolModeRetryCount(), failoverErr)
				switch action {
				case FailoverContinue:
					continue
				case FailoverExhausted:
					h.handleFailoverExhausted(c, fs.LastFailoverErr, service.PlatformGemini, streamStarted)
					return
				case FailoverCanceled:
					failoverClientGone(c)
					return
				}
			}
			upstreamErrorAlreadyCommunicated := gatewayForwardErrorAlreadyCommunicated(c, writerSizeBeforeForward, err)
			wroteFallback := false
			if !upstreamErrorAlreadyCommunicated {
				wroteFallback = h.ensureForwardErrorResponseForError(c, err, streamStarted)
			}
			forwardFailedFields := []zap.Field{
				zap.Int64("account_id", account.ID),
				zap.String("account_name", account.Name),
				zap.String("account_platform", account.Platform),
				zap.Bool("fallback_error_response_written", wroteFallback),
				zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
				zap.Error(err),
			}
			if account.Proxy != nil {
				forwardFailedFields = append(forwardFailedFields,
					zap.Int64("proxy_id", account.Proxy.ID),
					zap.String("proxy_name", account.Proxy.Name),
					zap.String("proxy_host", account.Proxy.Host),
					zap.Int("proxy_port", account.Proxy.Port),
				)
			} else if account.ProxyID != nil {
				forwardFailedFields = append(forwardFailedFields, zap.Int64p("proxy_id", account.ProxyID))
			}
			reqLog.Error("gateway.forward_failed", forwardFailedFields...)
			// TK: passive availability failure tap (R-004 — extracts upstream HTTP status from UpstreamFailoverError)
			TkRecordFailureFromErr(h.gatewayService, c.Request.Context(), account.Platform, reqModel, account.ID, err)
			return
		}

		// RPM 计数递增（Forward 成功后）
		// 注意：TOCTOU 竞态是已知且可接受的设计权衡，与其它调度 soft-limit 模式一致。
		// 在高并发下可能短暂超出 RPM 限制，但不会导致请求失败。
		if (account.IsAnthropicOAuthOrSetupToken() || account.IsKiro()) && account.GetBaseRPM() > 0 {
			if err := h.gatewayService.IncrementAccountRPM(c.Request.Context(), account.ID); err != nil {
				reqLog.Warn("gateway.rpm_increment_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			}
		}
		setOpsForwardResultContext(c, result.UpstreamModel, reqModel)
		setOpsClaudeUsageContext(c, result.Usage)

		// 捕获请求信息（用于异步记录，避免在 goroutine 中访问 gin.Context）
		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		requestPayloadHash := service.HashUsageRequestPayload(body)
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)

		if result.ReasoningEffort == nil {
			result.ReasoningEffort = service.NormalizeClaudeOutputEffort(parsedReq.OutputEffort)
		}
		// 国产模型 thinking-enabled 默认 effort 填充：Kimi/GLM/MiniMax 这些不支持 effort 档位的
		// passback-required 上游，仅要 thinking 启用且 OutputEffort 未明确传递时，在 usage_log 写 "high"
		// 避免该字段长期为 NULL（详见 DefaultEffortForThinkingEnabled 文档）。
		if result.ReasoningEffort == nil && parsedReq.ThinkingEnabled {
			protocolModel := result.UpstreamModel
			if protocolModel == "" {
				protocolModel = result.Model
			}
			result.ReasoningEffort = service.DefaultEffortForThinkingEnabled(protocolModel)
		}

		// 使用量记录通过有界 worker 池提交，避免请求热路径创建无界 goroutine。
		// ForceCacheBilling 提前拍成标量，避免 worker 闭包保活 failover 状态里的响应体。
		forceCacheBilling := fs.ForceCacheBilling
		quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
		sessionID := service.ExtractClientSessionID(c)
		gatewayLatencyMs := tkSnapshotGatewayTransferLatencyMs(c)
		h.submitUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
			if err := h.gatewayService.RecordUsage(ctx, &service.RecordUsageInput{
				Result:             result,
				QuotaPlatform:      quotaPlatform,
				APIKey:             apiKey,
				User:               apiKey.User,
				Account:            account,
				Subscription:       subscription,
				PricingAt:          pricingAt,
				InboundEndpoint:    inboundEndpoint,
				UpstreamEndpoint:   upstreamEndpoint,
				UserAgent:          userAgent,
				IPAddress:          clientIP,
				SessionID:          sessionID,
				RequestPayloadHash: requestPayloadHash,
				ForceCacheBilling:  forceCacheBilling,
				APIKeyService:      h.apiKeyService,
				GatewayLatencyMs:   gatewayLatencyMs,
				ChannelUsageFields: clientRequestedUsageFields(c, channelMapping, reqModel, result.UpstreamModel),
			}); err != nil {
				logger.L().With(
					zap.String("component", "handler.gateway.messages"),
					zap.Int64("user_id", subject.UserID),
					zap.Int64("api_key_id", apiKey.ID),
					zap.Any("group_id", apiKey.GroupID),
					zap.String("model", reqModel),
					zap.Int64("account_id", account.ID),
				).Error("gateway.record_usage_failed", zap.Error(err))
			}
		})
		return
	}
}

package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ImageGenerations handles POST /v1/images/generations for OpenAI-platform API keys.
func (h *OpenAIGatewayHandler) ImageGenerations(c *gin.Context) {
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)

	requestStart := time.Now()

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.images_generations",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		writeReadRequestBodyError(c, err, h.errorResponse)
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	if !gjson.ValidBytes(body) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	reqLog = reqLog.With(zap.String("model", reqModel))

	setOpsRequestModelAndBody(c, reqModel, false, body)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(false, false)))

	if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIImages, reqModel, body); decision != nil && !decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}

	// TK: pre-flight body-size guard (see gateway_handler_tk_body_guard.go).
	if reject, msg := TkEvalBodyGuard(reqLog, h.cfg.Gateway.UpstreamBodyGuards, domain.PlatformOpenAI, reqModel, len(body)); reject {
		h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", msg)
		return
	}

	// TK: unpriced media is not served — see openai_gateway_service_tk_media_unpriced_guard.go.
	if h.gatewayService.TkImageModelUnpriced(reqModel, apiKey.Group) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", service.TkUnpricedMediaModelMessage(reqModel, "image"))
		return
	}

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	// TK: pre-flight balance hold (concurrent-overdraft fix; see
	// openai_gateway_handler_tk_hold.go). Image holds reserve the requested
	// image count at the tier-max price; refund ownership is handed to the
	// usage-record task at submit time. Balance users only.
	hold, holdReject := h.tkApplyImageHold(c, apiKey, reqModel, int(gjson.GetBytes(body, "n").Int()))
	if holdReject {
		h.errorResponse(c, http.StatusForbidden, "insufficient_balance", tkInsufficientBalanceForHoldMsg)
		return
	}
	defer hold.ReleaseUnlessSettling()

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("openai_images_generations.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, _ := billingErrorDetails(err)
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	sessionHash := h.gatewayService.GenerateSessionHash(c, body)

	maxAccountSwitches := h.maxAccountSwitches
	switchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	profitVetoCount := 0

	for {
		c.Set("openai_v1_json_fallback_model", "")
		reqLog.Debug("openai_images_generations.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		selectionModel := reqModel
		selectionCtx, groupName := h.tkOpenAIChatSelectionCtx(c, apiKey, reqModel)
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithScheduler(
			selectionCtx,
			apiKey.GroupID,
			"",
			sessionHash,
			reqModel,
			failedAccountIDs,
			service.OpenAIUpstreamTransportAny,
			false,
		)
		if err != nil {
			reqLog.Warn("openai_images_generations.account_select_failed",
				tkSelectionFailureLogFields(err,
					zap.Error(err),
					zap.Int("excluded_account_count", len(failedAccountIDs)),
				)...,
			)
			if len(failedAccountIDs) == 0 {
				defaultModel := ""
				if apiKey.Group != nil {
					defaultModel = apiKey.Group.DefaultMappedModel
				}
				if defaultModel != "" && defaultModel != reqModel {
					selectionModel = defaultModel
					reqLog.Info("openai_images_generations.fallback_to_default_model",
						zap.String("default_mapped_model", defaultModel),
					)
					selection, scheduleDecision, err = h.gatewayService.SelectAccountWithScheduler(
						c.Request.Context(),
						apiKey.GroupID,
						"",
						sessionHash,
						defaultModel,
						failedAccountIDs,
						service.OpenAIUpstreamTransportAny,
						false,
					)
					if err == nil && selection != nil {
						c.Set("openai_v1_json_fallback_model", defaultModel)
					}
				}
				if err != nil {
					status, errType, msg := openAICompatFirstAttemptSelectionFailure(c, h.gatewayService, apiKey, selectionModel, reqModel, err)
					h.handleStreamingAwareError(c, status, errType, msg, streamStarted)
					return
				}
			} else {
				if lastFailoverErr != nil {
					h.handleFailoverExhausted(c, lastFailoverErr, streamStarted)
				} else {
					h.handleStreamingAwareError(c, http.StatusBadGateway, "api_error", "Upstream request failed", streamStarted)
				}
				return
			}
		}
		if selection == nil || selection.Account == nil {
			status, errType, msg := openAICompatFirstAttemptSelectionFailure(c, h.gatewayService, apiKey, selectionModel, reqModel, nil)
			h.handleStreamingAwareError(c, status, errType, msg, streamStarted)
			return
		}
		account := selection.Account
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai_images_generations.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		_ = scheduleDecision
		setOpsSelectedAccountFrom(c, account)
		openAIMarkAffinitySelected(c, groupName, account.ID)

		accountReleaseFunc, slotResult := h.acquireResponsesAccountSlot(c, apiKey.GroupID, sessionHash, selection, false, &streamStarted, reqLog)
		if slotResult == openAISlotAcquireProfitVetoed {
			if !recordOpenAIProfitVeto(failedAccountIDs, account.ID, &profitVetoCount) {
				h.handleOpenAIProfitVetoExhausted(c, streamStarted, reqLog, profitVetoCount)
				return
			}
			continue
		}
		if slotResult != openAISlotAcquireOK {
			return
		}

		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		forwardStart := time.Now()

		defaultMappedModel := resolveOpenAIForwardDefaultMappedModel(apiKey, c.GetString("openai_v1_json_fallback_model"))
		forwardBody := body
		if channelMapping.Mapped {
			forwardBody = h.gatewayService.ReplaceModelInBody(body, channelMapping.MappedModel)
		}
		writerSizeBeforeForward := c.Writer.Size()
		TkSetBridgeGinAuth(c, subject.UserID, groupName)
		logOpenAIImageGenerationRequestAudit(c, apiKey, subject.UserID, account, body, forwardBody)
		result, err := h.gatewayService.ForwardAsImageGenerationsDispatched(c.Request.Context(), c, account, forwardBody, defaultMappedModel)

		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}
		tkRecordForwardResponseTail(c, forwardStart)

		if err != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				h.gatewayService.ReportOpenAIAccountScheduleResult(account, account.GetMappedModel(reqModel), false, nil)
				if failoverErr.RetryableOnSameAccount {
					retryLimit := account.GetPoolModeRetryCount()
					if sameAccountRetryCount[account.ID] < retryLimit {
						sameAccountRetryCount[account.ID]++
						reqLog.Warn("openai_images_generations.pool_mode_same_account_retry",
							zap.Int64("account_id", account.ID),
							zap.Int("upstream_status", failoverErr.StatusCode),
							zap.Int("retry_limit", retryLimit),
							zap.Int("retry_count", sameAccountRetryCount[account.ID]),
						)
						select {
						case <-c.Request.Context().Done():
							return
						case <-time.After(sameAccountRetryDelay):
						}
						continue
					}
				}
				h.gatewayService.RecordOpenAIAccountSwitch()
				failedAccountIDs[account.ID] = struct{}{}
				lastFailoverErr = failoverErr
				if switchCount >= maxAccountSwitches {
					h.handleFailoverExhausted(c, failoverErr, streamStarted)
					return
				}
				switchCount++
				reqLog.Warn("openai_images_generations.upstream_failover_switching",
					zap.Int64("account_id", account.ID),
					zap.Int("upstream_status", failoverErr.StatusCode),
					zap.Int("switch_count", switchCount),
					zap.Int("max_switches", maxAccountSwitches),
				)
				continue
			}
			if TkTryWriteNewAPIRelayErrorJSON(c, err, streamStarted, writerSizeBeforeForward) {
				h.gatewayService.ReportOpenAIAccountScheduleResult(account, account.GetMappedModel(reqModel), false, nil)
				reqLog.Warn("openai_images_generations.forward_failed",
					zap.Int64("account_id", account.ID),
					zap.Bool("fallback_error_response_written", false),
					zap.Error(err),
				)
				return
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, account.GetMappedModel(reqModel), false, nil)
			wroteFallback := h.ensureForwardErrorResponse(c, streamStarted)
			reqLog.Warn("openai_images_generations.forward_failed",
				zap.Int64("account_id", account.ID),
				zap.Bool("fallback_error_response_written", wroteFallback),
				zap.Error(err),
			)
			return
		}
		if result != nil {
			setOpsForwardResultContext(c, result.UpstreamModel, reqModel)
			setOpsOpenAIUsageContext(c, result.Usage)
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, account.GetMappedModel(reqModel), true, result.FirstTokenMs)
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, account.GetMappedModel(reqModel), true, nil)
		}
		openAIRecordAffinitySuccess(c, account.ID)

		h.tkSubmitOpenAISimpleForwardUsage(tkOpenAISimpleUsageSubmitInput{
			C:                  c,
			APIKey:             apiKey,
			Account:            account,
			Subscription:       subscription,
			Subject:            subject,
			Hold:               hold,
			Result:             result,
			ReqModel:           reqModel,
			ChannelMapping:     channelMapping,
			LogComponent:       "handler.openai_gateway.images_generations",
			LogFailedEventName: "openai_images_generations.record_usage_failed",
		})
		reqLog.Debug("openai_images_generations.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

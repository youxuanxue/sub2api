package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// AudioSpeech handles POST /v1/audio/speech for OpenAI-compat platforms.
// Ali Token Plan TTS is forwarded natively (SpeechSynthesizer); other platforms 404.
func (h *OpenAIGatewayHandler) AudioSpeech(c *gin.Context) {
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
		"handler.openai_gateway.audio_speech",
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

	inputText := gjson.GetBytes(body, "input").String()
	if strings.TrimSpace(inputText) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "input is required")
		return
	}

	setOpsRequestModelAndBody(c, reqModel, false, body)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(false, false)))

	auditBody := body
	if b, err := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": inputText}},
	}); err == nil {
		auditBody = b
	}
	if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, reqModel, auditBody); decision != nil && !decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}

	if reject, msg := TkEvalBodyGuard(reqLog, h.cfg.Gateway.UpstreamBodyGuards, domain.PlatformOpenAI, reqModel, len(body)); reject {
		h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", msg)
		return
	}

	if h.gatewayService.TkTTSModelUnpriced(reqModel, apiKey.Group) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", service.TkUnpricedMediaModelMessage(reqModel, "tts"))
		return
	}

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	hold, holdReject := h.tkApplyTTSHold(c, apiKey, reqModel, utf8.RuneCountInString(inputText))
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
		reqLog.Info("openai_audio_speech.billing_eligibility_check_failed", zap.Error(err))
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
		reqLog.Debug("openai_audio_speech.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		selectionCtx, groupName := h.tkOpenAIChatSelectionCtx(c, apiKey, reqModel)
		selection, _, err := h.gatewayService.SelectAccountWithScheduler(
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
			if len(failedAccountIDs) == 0 {
				status, errType, msg := openAICompatFirstAttemptSelectionFailure(c, h.gatewayService, apiKey, reqModel, reqModel, err)
				h.handleStreamingAwareError(c, status, errType, msg, streamStarted)
				return
			}
			if lastFailoverErr != nil {
				h.handleFailoverExhausted(c, lastFailoverErr, streamStarted)
			} else {
				h.handleStreamingAwareError(c, http.StatusBadGateway, "api_error", "Upstream request failed", streamStarted)
			}
			return
		}
		if selection == nil || selection.Account == nil {
			status, errType, msg := openAICompatFirstAttemptSelectionFailure(c, h.gatewayService, apiKey, reqModel, reqModel, nil)
			h.handleStreamingAwareError(c, status, errType, msg, streamStarted)
			return
		}

		account := selection.Account
		if !service.IsNewAPIAliTokenPlanAccount(account) {
			// Native SpeechSynthesizer forward is Token Plan only; mis-mapped
			// PAYG/other Ali accounts must not abort the loop before a capable
			// account is tried.
			failedAccountIDs[account.ID] = struct{}{}
			h.gatewayService.RecordOpenAIAccountSwitch()
			if switchCount >= maxAccountSwitches {
				h.handleStreamingAwareError(c, http.StatusBadGateway, "api_error", "No Ali Token Plan account available for audio speech", streamStarted)
				return
			}
			switchCount++
			continue
		}
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
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

		forwardBody := body
		if channelMapping.Mapped {
			forwardBody = h.gatewayService.ReplaceModelInBody(body, channelMapping.MappedModel)
		}
		writerSizeBeforeForward := c.Writer.Size()
		result, err := h.gatewayService.ForwardAliTokenPlanTTS(c.Request.Context(), c, account, forwardBody)

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
				continue
			}
			if TkTryWriteNewAPIRelayErrorJSON(c, err, streamStarted, writerSizeBeforeForward) {
				h.gatewayService.ReportOpenAIAccountScheduleResult(account, account.GetMappedModel(reqModel), false, nil)
				return
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account, account.GetMappedModel(reqModel), false, nil)
			wroteFallback := h.ensureForwardErrorResponse(c, streamStarted)
			reqLog.Warn("openai_audio_speech.forward_failed",
				zap.Int64("account_id", account.ID),
				zap.Bool("fallback_error_response_written", wroteFallback),
				zap.Error(err),
			)
			return
		}

		if result != nil {
			setOpsForwardResultContext(c, result.UpstreamModel, reqModel)
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
			LogComponent:       "handler.openai_gateway.audio_speech",
			LogFailedEventName: "openai_audio_speech.record_usage_failed",
		})
		return
	}
}

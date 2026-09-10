package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/audio"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// AudioSpeech handles POST /v1/audio/speech for OpenAI-compat platforms.
// Supported Token Plan accounts use provider-native speech synthesis.
func (h *OpenAIGatewayHandler) AudioSpeech(c *gin.Context) {
	h.audioRequest(c, false)
}

func (h *OpenAIGatewayHandler) AudioTranscriptions(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), service.AudioTranscriptionTimeout)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	h.audioRequest(c, true)
}

// Both audio operations share admission, scheduling, failover and settlement.
func (h *OpenAIGatewayHandler) audioRequest(c *gin.Context, transcription bool) {
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)

	requestStart := time.Now()
	component := "handler.openai_gateway.audio_speech"
	if transcription {
		component = "handler.openai_gateway.audio_transcriptions"
	}

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
		component,
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	if transcription {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, audio.MaxUploadBytes+64*1024)
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
	var recording []byte
	var asr *service.AudioTranscriptionRequest
	if transcription {
		asr, recording, err = parseAudioTranscription(body, c.GetHeader("Content-Type"))
		if err != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		body, _ = json.Marshal(map[string]any{"model": asr.Model, "response_format": asr.ResponseFormat, "audio_bytes": len(recording)})
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
	if reqModel == "doubao-seed-tts-2.0" {
		if err := service.ValidateVolcEnginePlanTTSRequest(body); err != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	}

	inputText := gjson.GetBytes(body, "input").String()
	if !transcription && strings.TrimSpace(inputText) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "input is required")
		return
	}

	setOpsRequestModelAndBody(c, reqModel, false, body)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(false, false)))

	auditBody := body
	if !transcription {
		if b, err := json.Marshal(map[string]any{
			"messages": []map[string]any{{"role": "user", "content": inputText}},
		}); err == nil {
			auditBody = b
		}
	}
	if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, reqModel, auditBody); decision != nil && !decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}

	if reject, msg := TkEvalBodyGuard(reqLog, h.cfg.Gateway.UpstreamBodyGuards, domain.PlatformOpenAI, reqModel, len(body)); reject {
		h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", msg)
		return
	}

	unpriced := h.gatewayService.TkTTSModelUnpriced(reqModel, apiKey.Group)
	mode := "tts"
	if transcription {
		unpriced = h.gatewayService.TkSTTModelUnpriced(reqModel, apiKey.Group)
		mode = "stt"
	}
	if unpriced {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", service.TkUnpricedMediaModelMessage(reqModel, mode))
		return
	}

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)
	if transcription && channelMapping.Mapped && channelMapping.MappedModel != reqModel {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "unsupported transcription model mapping")
		return
	}

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}
	if transcription {
		normalizeCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		asr.PCM, err = audio.Normalize(normalizeCtx, recording)
		cancel()
		if err != nil {
			if h.audioRequestContextDone(c) {
				return
			}
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	}
	var hold *tkHoldHandle
	var holdReject bool
	if transcription {
		hold, holdReject = h.tkApplyHold(c, apiKey, "", func(requestID string) (bool, bool) {
			return h.gatewayService.TkReserveSTTHold(c.Request.Context(), requestID, reqModel, apiKey.User, apiKey, float64(len(asr.PCM))/audio.PCMBytesPerSecond)
		})
	} else {
		hold, holdReject = h.tkApplyTTSHold(c, apiKey, reqModel, utf8.RuneCountInString(inputText))
	}
	if holdReject {
		h.errorResponse(c, http.StatusForbidden, "insufficient_balance", tkInsufficientBalanceForHoldMsg)
		return
	}
	defer hold.ReleaseUnlessSettling()

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
		if h.audioRequestContextDone(c) {
			return
		}
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
		supported := service.SupportsNativeAudioSpeech(account)
		if transcription {
			supported = service.SupportsNativeAudioTranscription(account) && account.GetMappedModel(reqModel) == service.VolcEnginePlanASRModel
		}
		if !supported {
			// Continue selection when a mapping includes an unsupported provider.
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
				selection.ReleaseFunc = nil
			}
			failedAccountIDs[account.ID] = struct{}{}
			h.gatewayService.RecordOpenAIAccountSwitch()
			if switchCount >= maxAccountSwitches {
				h.handleStreamingAwareError(c, http.StatusBadGateway, "api_error", "No supported Token Plan account available for this audio operation", streamStarted)
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
		var result *service.OpenAIForwardResult
		if transcription {
			result, err = h.gatewayService.ForwardNativeAudioTranscription(c.Request.Context(), c, account, asr)
		} else {
			result, err = h.gatewayService.ForwardNativeAudioSpeech(c.Request.Context(), c, account, forwardBody)
		}

		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}
		tkRecordForwardResponseTail(c, forwardStart)

		if err != nil {
			if h.audioRequestContextDone(c) {
				return
			}
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				h.gatewayService.ReportOpenAIAccountScheduleResult(account, account.GetMappedModel(reqModel), false, nil)
				if failoverErr.RetryableOnSameAccount {
					retryLimit := account.GetPoolModeRetryCount()
					if sameAccountRetryCount[account.ID] < retryLimit {
						sameAccountRetryCount[account.ID]++
						select {
						case <-c.Request.Context().Done():
							h.audioRequestContextDone(c)
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
			wroteFallback := h.ensureForwardErrorResponseForError(c, err, streamStarted)
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
			LogComponent:       component,
			LogFailedEventName: "openai_audio_speech.record_usage_failed",
		})
		return
	}
}

func (h *OpenAIGatewayHandler) audioRequestContextDone(c *gin.Context) bool {
	err := c.Request.Context().Err()
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) && !c.Writer.Written() {
		h.errorResponse(c, http.StatusGatewayTimeout, "timeout_error", "Audio request timed out")
	}
	return true
}

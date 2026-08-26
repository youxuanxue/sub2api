package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ChatCompletions handles OpenAI Chat Completions API endpoint for Anthropic platform groups.
// POST /v1/chat/completions
// This converts Chat Completions requests to Anthropic format (via Responses format chain),
// forwards to Anthropic upstream, and converts responses back to Chat Completions format.
func (h *GatewayHandler) ChatCompletions(c *gin.Context) {
	streamStarted := false

	requestStart := time.Now()

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.chatCompletionsErrorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.chatCompletionsErrorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.gateway.chat_completions",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	// Read request body
	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		writeReadRequestBodyError(c, err, h.chatCompletionsErrorResponse)
		return
	}

	if len(body) == 0 {
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	setOpsRequestContext(c, "", false)

	// Validate JSON
	if !gjson.ValidBytes(body) {
		logRequestBodyParseFailure(reqLog, body, nil)
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	// Extract model and stream
	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	ensureCompositeTargetPlatform(c, apiKey, reqModel)
	if !compositeTargetPlatformResolved(c, apiKey, reqModel) {
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by composite groups")
		return
	}
	reqStream, ok := parseOpenAICompatibleStream(body)
	if !ok {
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", invalidStreamFieldTypeMessage)
		return
	}
	if service.IsGPTImageGenerationModel(reqModel) {
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "This model is not supported on the Chat Completions endpoint")
		return
	}
	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))

	setOpsRequestModelAndBody(c, reqModel, reqStream, body)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(reqStream, false)))
	pricingCtx, pricingAt := service.WithGatewayTokenRequestPricing(c.Request.Context())
	c.Request = c.Request.WithContext(pricingCtx)

	// 解析渠道级模型映射
	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)

	// Claude Code only restriction.
	// TK: when a CC-only group declares a valid fallback_group_id, route non-CC
	// OpenAI-compat traffic to that fallback group instead of hard-403'ing.
	if apiKey.Group != nil && apiKey.Group.ClaudeCodeOnly {
		writeForbidden := func() {
			h.chatCompletionsErrorResponse(c, http.StatusForbidden, "permission_error", tkCCOnlyForbiddenMessage)
		}
		writeBillingError := func(status int, code, message string) {
			h.chatCompletionsErrorResponse(c, status, code, message)
		}
		fallbackAPIKey, handled := h.tkResolveCCOnlyFallback(c, apiKey, reqLog, writeForbidden, writeBillingError)
		if handled {
			return
		}
		apiKey = fallbackAPIKey
		channelMapping, _ = h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)
	}

	if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, reqModel, body); decision != nil && !decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}

	// Error passthrough binding
	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())

	userReleaseFunc, err := h.concurrencyHelper.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted)
	if err != nil {
		reqLog.Warn("gateway.cc.user_slot_acquire_failed", zap.Error(err))
		h.handleConcurrencyError(c, err, "user", streamStarted)
		return
	}
	userReleaseFunc = wrapReleaseOnDone(c.Request.Context(), userReleaseFunc)
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// 2. Re-check billing
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("gateway.cc.billing_check_failed", zap.Error(err))
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.chatCompletionsErrorResponse(c, status, code, message)
		return
	}

	// Parse request for session hash
	bodyRef := service.NewRequestBodyRef(body)
	parsedReq, _ := service.ParseGatewayRequest(bodyRef, "chat_completions")
	if parsedReq == nil {
		parsedReq = &service.ParsedRequest{Model: reqModel, Stream: reqStream, Body: bodyRef}
	}
	reasoningEffort := service.ExtractChatCompletionsReasoningEffortFromBody(body)
	if service.OpenAIReasoningEnablesThinking(reasoningEffort, body) {
		parsedReq.ThinkingEnabled = true
	}
	c.Request = c.Request.WithContext(service.WithThinkingEnabled(
		c.Request.Context(), parsedReq.ThinkingEnabled, h.metadataBridgeEnabled(),
	))
	canonicalRequest, err := newCanonicalProtocolRequest(
		protocolrouter.ProtocolChatCompletions,
		protocolrouter.ResponsesPathNone,
		reqModel,
		reqStream,
		body,
	)
	if err != nil {
		h.chatCompletionsErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	c.Request = c.Request.WithContext(service.WithProtocolRouting(c.Request.Context(), h.protocolRouter, canonicalRequest))
	TkPrepareParsedRequestSessionInputs(c, apiKey, parsedReq)
	sessionHash := h.gatewayService.GenerateSessionHash(parsedReq)
	groupPlatform := effectiveAPIKeyPlatform(c, apiKey)
	groupUsesGeminiCompat := service.UsesGeminiNativeOpenAICompat(groupPlatform, reqModel)
	selectionSessionHash := sessionHash
	if groupUsesGeminiCompat && selectionSessionHash != "" {
		selectionSessionHash = "gemini:" + selectionSessionHash
	}
	// 3. Account selection + failover loop
	fs := NewFailoverState(h.maxAccountSwitches, false)
	if groupUsesGeminiCompat {
		fs = NewFailoverState(h.maxAccountSwitchesGemini, false)
	}

	for {
		routingStart := time.Now()
		if c.Request.Context().Err() != nil {
			return
		}
		selection, err := h.gatewayService.SelectAccountWithLoadAwareness(c.Request.Context(), apiKey.GroupID, selectionSessionHash, reqModel, fs.FailedAccountIDs, "", int64(0))
		if err != nil {
			if len(fs.FailedAccountIDs) == 0 {
				markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				tkStatus, tkType, tkMsg := tkSelectFailureStatusMessage(c, err, reqModel)
				h.chatCompletionsErrorResponse(c, tkStatus, tkType, tkMsg)
				return
			}
			action := fs.HandleSelectionExhausted(c.Request.Context(), errors.Is(err, service.ErrThinPoolAllExcluded))
			switch action {
			case FailoverContinue:
				continue
			case FailoverCanceled:
				failoverClientGone(c)
				return
			default:
				if fs.LastFailoverErr != nil {
					h.handleCCFailoverExhausted(c, fs.LastFailoverErr, streamStarted)
				} else {
					h.chatCompletionsErrorResponse(c, http.StatusBadGateway, "server_error",
						service.GatewayFailoverClientMessage(http.StatusBadGateway))
				}
				return
			}
		}
		account := selection.Account
		setOpsSelectedAccountFrom(c, account)

		// 4. Acquire account concurrency slot
		accountReleaseFunc := selection.ReleaseFunc
		if !selection.Acquired {
			if selection.WaitPlan == nil {
				markOpsRoutingCapacityLimited(c)
				h.chatCompletionsErrorResponse(c, tkNoAvailableAccounts(c), "api_error", "No available accounts")
				return
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
				reqLog.Warn("gateway.cc.account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
				h.handleConcurrencyError(c, err, "account", streamStarted)
				return
			}
		}
		// 终检与准入后绑定使用选号结果携带的门（见 responses 同名注释）。
		admissionCtx := service.ContextWithSelectionProfitGate(c.Request.Context(), selection)
		latest, vetoed, reason := h.gatewayService.GatewayProfitControlVetoLatest(admissionCtx, account)
		if vetoed {
			if accountReleaseFunc != nil {
				accountReleaseFunc()
			}
			reqLog.Debug("gateway.cc.account_slot_profit_vetoed", zap.Int64("account_id", account.ID), zap.String("reason", reason))
			if fs.RecordProfitVeto(account.ID) == FailoverExhausted {
				reqLog.Warn("gateway.cc.profit_veto_attempts_exhausted", zap.Int("profit_veto_count", fs.ProfitVetoCount()))
				h.chatCompletionsErrorResponse(c, http.StatusServiceUnavailable, "api_error", profitVetoExhaustedMessage)
				return
			}
			continue
		}
		account = latest
		selection.Account = latest
		if selection.ProfitGateActive() {
			if err := h.gatewayService.BindStickySessionAfterProfitAdmission(admissionCtx, apiKey.GroupID, selectionSessionHash, account.ID); err != nil {
				reqLog.Warn("gateway.cc.bind_sticky_session_after_profit_admission_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			}
		}
		accountReleaseFunc = wrapReleaseOnDone(c.Request.Context(), accountReleaseFunc)

		if groupPlatform == service.PlatformGemini && account.Platform != service.PlatformGemini {
			if accountReleaseFunc != nil {
				accountReleaseFunc()
			}
			fs.FailedAccountIDs[account.ID] = struct{}{}
			continue
		}
		if groupPlatform == service.PlatformAntigravity && account.Platform != service.PlatformAntigravity {
			if accountReleaseFunc != nil {
				accountReleaseFunc()
			}
			fs.FailedAccountIDs[account.ID] = struct{}{}
			continue
		}

		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())

		// 5. Forward request
		writerSizeBeforeForward := c.Writer.Size()
		forwardStart := time.Now()
		var result *service.ForwardResult
		setActualUpstreamEndpoint(c, "")
		value, executeErr := service.ExecuteSelectedProtocol(
			c.Request.Context(),
			h.protocolRouter,
			selection,
			account,
			h.gatewayService.ValidateProtocolEndpoint,
			service.ProtocolExecutors{
				NonGoverned: func(executionCtx context.Context, _ protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
					forwardBody := request.Body()
					if channelMapping.Mapped {
						forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
					}
					if service.UsesGeminiNativeOpenAICompat(account.Platform, reqModel) {
						if h.geminiCompatService == nil {
							return nil, errors.New("gemini compatibility service is not configured")
						}
						return h.geminiCompatService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody)
					}
					if shouldUseAntigravityCompat(account) {
						if h.antigravityGatewayService == nil {
							return nil, errors.New("antigravity compatibility service is not configured")
						}
						setActualUpstreamEndpoint(c, EndpointAntigravityGenerateContent)
						return h.antigravityGatewayService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody, parsedReq)
					}
					return h.gatewayService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody, parsedReq)
				},
				ChatIdentity: func(executionCtx context.Context, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
					forwardBody := request.Body()
					if channelMapping.Mapped {
						forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
					}
					setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
					openAIResult, forwardErr := h.openAIGatewayService.ForwardAsChatCompletionsDispatched(executionCtx, c, account, forwardBody, "", channelMapping.MappedModel)
					return service.ForwardResultFromOpenAI(openAIResult), forwardErr
				},
				ChatToResponses: func(executionCtx context.Context, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
					forwardBody := request.Body()
					if channelMapping.Mapped {
						forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
					}
					setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
					openAIResult, forwardErr := h.openAIGatewayService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody, "", channelMapping.MappedModel)
					return service.ForwardResultFromOpenAI(openAIResult), forwardErr
				},
				ChatToMessages: func(executionCtx context.Context, plan protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
					forwardBody := request.Body()
					if channelMapping.Mapped {
						forwardBody = h.gatewayService.ReplaceModelInBody(forwardBody, channelMapping.MappedModel)
					}
					setActualUpstreamEndpoint(c, protocolPlanEndpoint(plan.Endpoint()))
					return h.gatewayService.ForwardAsChatCompletions(executionCtx, c, account, forwardBody, parsedReq)
				},
			},
		)
		err = executeErr
		if value != nil {
			result, _ = value.(*service.ForwardResult)
		}
		tkRecordForwardResponseTail(c, forwardStart)

		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}

		if err != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				if c.Writer.Size() != writerSizeBeforeForward {
					h.handleCCFailoverExhausted(c, failoverErr, true)
					return
				}
				action := fs.HandleFailoverError(c.Request.Context(), h.gatewayService, account.ID, account.Platform, account.GetPoolModeRetryCount(), failoverErr)
				switch action {
				case FailoverContinue:
					continue
				case FailoverExhausted:
					h.handleCCFailoverExhausted(c, fs.LastFailoverErr, streamStarted)
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
			reqLog.Error("gateway.cc.forward_failed",
				zap.Int64("account_id", account.ID),
				zap.Bool("fallback_error_response_written", wroteFallback),
				zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
				zap.Error(err),
			)
			TkRecordFailureFromErr(h.gatewayService, c.Request.Context(), account.Platform, reqModel, account.ID, err)
			return
		}

		// 6. Record usage
		setOpsClaudeUsageContext(c, result.Usage)
		setOpsForwardResultContext(c, result.UpstreamModel, reqModel)
		h.tkSubmitClaudeGatewayForwardUsage(tkClaudeGatewayForwardUsageInput{
			C:                  c,
			APIKey:             apiKey,
			Account:            account,
			Subscription:       subscription,
			Result:             result,
			ReqModel:           reqModel,
			Body:               body,
			PricingAt:          pricingAt,
			ChannelUsageFields: clientRequestedUsageFields(c, channelMapping, reqModel, result.UpstreamModel),
			ReqLog:             reqLog,
			LogFailedEvent:     "gateway.cc.record_usage_failed",
		})
		return
	}
}

// chatCompletionsErrorResponse writes an error in OpenAI Chat Completions format.
func (h *GatewayHandler) chatCompletionsErrorResponse(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// handleCCFailoverExhausted writes a failover-exhausted error in CC format.
func (h *GatewayHandler) handleCCFailoverExhausted(c *gin.Context, lastErr *service.UpstreamFailoverError, streamStarted bool) {
	if streamStarted {
		return
	}
	if lastErr != nil {
		copyFailoverRetryAfter(c, lastErr.ResponseHeaders)
	}
	if lastErr != nil && lastErr.IsCredentialFailure() {
		status, message := credentialFailoverClientResponse(lastErr)
		h.chatCompletionsErrorResponse(c, status, "server_error", message)
		return
	}
	if lastErr != nil && lastErr.IsOpenAICapacityShed() && strings.TrimSpace(lastErr.ClientMessage) != "" {
		status := lastErr.ClientStatusCode
		if status <= 0 {
			status = http.StatusServiceUnavailable
		}
		h.chatCompletionsErrorResponse(c, status, "server_error", lastErr.ClientMessage)
		return
	}
	statusCode := http.StatusBadGateway
	if lastErr != nil && lastErr.StatusCode > 0 {
		statusCode = lastErr.StatusCode
	}
	if lastErr != nil && service.IsOpenAISilentRefusalErrorBody(lastErr.ResponseBody) {
		service.SetOpsUpstreamError(c, statusCode, service.OpenAISilentRefusalClientMessage(), "")
		h.chatCompletionsErrorResponse(c, http.StatusBadGateway, "upstream_error", service.OpenAISilentRefusalClientMessage())
		return
	}
	message := service.GatewayFailoverClientMessage(statusCode)
	if lastErr != nil && lastErr.ClientStatusCode > 0 {
		statusCode = lastErr.ClientStatusCode
		if lastErr.ClientMessage != "" {
			message = lastErr.ClientMessage
		}
	}
	h.chatCompletionsErrorResponse(c, statusCode, "server_error", message)
}

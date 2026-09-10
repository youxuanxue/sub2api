package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// GeminiV1BetaListModels proxies:
// GET /v1beta/models
func (h *GatewayHandler) GeminiV1BetaListModels(c *gin.Context) {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		googleError(c, http.StatusUnauthorized, "Invalid API key")
		return
	}
	if useCandidateModelDiscovery(h.tkCapabilities, apiKey) {
		capabilities, err := h.universalCapabilities(c.Request.Context(), apiKey, service.UniversalProtocolGemini)
		if err != nil {
			requestLogger(c, "gateway.gemini_models", zap.String("protocol", string(service.UniversalProtocolGemini))).Warn("capability_discovery_failed", zap.Error(err))
			googleError(c, http.StatusInternalServerError, "Model discovery unavailable")
			return
		}
		ids := directCustomCapabilityIDs(apiKey, capabilityModelIDs(capabilities, service.UniversalModalityChat))
		c.JSON(http.StatusOK, geminiModelsForCapabilityIDs(ids))
		return
	}
	// 检查平台：优先使用强制平台（/antigravity 路由），否则要求 gemini/antigravity 分组
	// TK: platform allow — see gemini_v1beta_handler_tk_platform.go
	forcePlatform, hasForcePlatform := middleware.GetForcePlatformFromContext(c)
	if !hasForcePlatform && !geminiV1BetaGroupPlatformAllowed(apiKey) {
		googleError(c, http.StatusBadRequest, "API key group cannot serve native Gemini requests")
		return
	}

	// TK: CatalogPolicy / force-platform branches — see gemini_v1beta_handler_tk_platform.go
	if h.tkGeminiV1BetaTryForceAntigravityListModels(c, forcePlatform) {
		return
	}

	account, err := h.geminiCompatService.SelectAccountForAIStudioEndpoints(c.Request.Context(), apiKey.GroupID)
	if err != nil {
		// 没有 gemini 账户，检查是否有 antigravity 账户可用
		hasAntigravity, _ := h.geminiCompatService.HasAntigravityAccounts(c.Request.Context(), apiKey.GroupID)
		if hasAntigravity {
			h.tkGeminiV1BetaListModelsCatalogFallback(c)
			return
		}
		markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
		status, _, message := tkSelectFailureStatusMessage(c, err, "")
		googleError(c, status, message)
		return
	}

	res, err := h.geminiCompatService.ForwardAIStudioGET(c.Request.Context(), account, "/v1beta/models")
	if err != nil {
		googleError(c, http.StatusBadGateway, err.Error())
		return
	}
	if shouldFallbackGeminiModels(res) {
		h.tkGeminiV1BetaListModelsCatalogFallback(c)
		return
	}
	if apiKey.Group != nil && apiKey.Group.ModelAllowlistEnabled() {
		if filtered, dropped, ok := filterUpstreamGeminiModelsBody(res.Body, apiKey.Group.ModelAllowlist); ok && dropped {
			// 只在确有条目被过滤时替换响应体；全命中或解析失败时保持原始响应，
			// 统一经 writeUpstreamResponse 写出（保留全部上游响应头）。
			res.Body = filtered
		}
	}
	writeUpstreamResponse(c, res)
}

// filterUpstreamGeminiModelsBody 按白名单过滤上游 /v1beta/models 响应中的
// models[].name，其余信封字段（如 nextPageToken）原样保留。
// 返回值：filtered 为过滤后的响应体；dropped 表示是否有条目被移除（全命中时
// 为 false，调用方应保持原始响应以完整透传上游头）；ok=false 表示解析失败，
// 调用方同样应透传原始响应。
func filterUpstreamGeminiModelsBody(body []byte, allowlist service.GroupModelAllowlist) (filtered []byte, dropped bool, ok bool) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false, false
	}
	rawModels, hasModels := envelope["models"]
	if !hasModels {
		return body, false, true
	}
	type geminiModelName struct {
		Name string `json:"name"`
	}
	var models []json.RawMessage
	if err := json.Unmarshal(rawModels, &models); err != nil {
		return nil, false, false
	}
	kept := make([]json.RawMessage, 0, len(models))
	for _, raw := range models {
		var model geminiModelName
		if err := json.Unmarshal(raw, &model); err != nil {
			return nil, false, false
		}
		if allowlist.Allows(model.Name) {
			kept = append(kept, raw)
		}
	}
	if len(kept) == len(models) {
		// 全部命中时直接透传原始响应体。
		return body, false, true
	}
	mergedModels, err := json.Marshal(kept)
	if err != nil {
		return nil, false, false
	}
	envelope["models"] = mergedModels
	merged, err := json.Marshal(envelope)
	if err != nil {
		return nil, false, false
	}
	return merged, true, true
}

// GeminiV1BetaGetModel proxies:
// GET /v1beta/models/{model}
func (h *GatewayHandler) GeminiV1BetaGetModel(c *gin.Context) {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		googleError(c, http.StatusUnauthorized, "Invalid API key")
		return
	}
	modelName := strings.TrimSpace(c.Param("model"))
	if modelName == "" {
		googleError(c, http.StatusBadRequest, "Missing model in URL")
		return
	}
	// 模型名会被拼进上游 URL 的 path，先在入口校验片段合规性，
	// 见 service/upstream_path_guard.go。
	if !service.IsSafeGeminiModelPathSegment(modelName) {
		googleError(c, http.StatusBadRequest, "Invalid model in URL")
		return
	}
	if useCandidateModelDiscovery(h.tkCapabilities, apiKey) {
		capabilities, err := h.universalCapabilities(c.Request.Context(), apiKey, service.UniversalProtocolGemini)
		if err != nil {
			requestLogger(c, "gateway.gemini_model", zap.String("model", modelName)).Warn("capability_discovery_failed", zap.Error(err))
			googleError(c, http.StatusInternalServerError, "Model discovery unavailable")
			return
		}
		for _, model := range geminiModelsForCapabilityIDs(capabilityModelIDs(capabilities, service.UniversalModalityChat)).Models {
			if model.Name == "models/"+modelName {
				c.JSON(http.StatusOK, model)
				return
			}
		}
		googleError(c, http.StatusNotFound, "Model not found")
		return
	}
	forcePlatform, hasForcePlatform := middleware.GetForcePlatformFromContext(c)
	if !hasForcePlatform && !geminiV1BetaGroupPlatformAllowed(apiKey) {
		googleError(c, http.StatusBadRequest, "API key group cannot serve native Gemini requests")
		return
	}
	if resolvedModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context()); ok && strings.TrimSpace(resolvedModel) != "" {
		modelName = strings.TrimSpace(resolvedModel)
	}

	// TK: CatalogPolicy / force-platform branches — see gemini_v1beta_handler_tk_platform.go
	if h.tkGeminiV1BetaTryForceAntigravityGetModel(c, forcePlatform, modelName) {
		return
	}

	account, err := h.geminiCompatService.SelectAccountForAIStudioEndpoints(c.Request.Context(), apiKey.GroupID)
	if err != nil {
		// 没有 gemini 账户，检查是否有 antigravity 账户可用
		hasAntigravity, _ := h.geminiCompatService.HasAntigravityAccounts(c.Request.Context(), apiKey.GroupID)
		if hasAntigravity {
			h.tkGeminiV1BetaGetModelAntigravityFallback(c, modelName)
			return
		}
		markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
		status, _, message := tkSelectFailureStatusMessage(c, err, modelName)
		googleError(c, status, message)
		return
	}

	res, err := h.geminiCompatService.ForwardAIStudioGET(c.Request.Context(), account, "/v1beta/models/"+modelName)
	if err != nil {
		googleError(c, http.StatusBadGateway, err.Error())
		return
	}
	if shouldFallbackGeminiModel(modelName, res) {
		h.tkGeminiV1BetaGetModelAntigravityFallback(c, modelName)
		return
	}
	writeUpstreamResponse(c, res)
}

// GeminiV1BetaModels proxies Gemini native REST endpoints like:
// POST /v1beta/models/{model}:generateContent
// POST /v1beta/models/{model}:streamGenerateContent?alt=sse
func (h *GatewayHandler) GeminiV1BetaModels(c *gin.Context) {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		googleError(c, http.StatusUnauthorized, "Invalid API key")
		return
	}
	authSubject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		googleError(c, http.StatusInternalServerError, "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.gemini_v1beta.models",
		zap.Int64("user_id", authSubject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	// 检查平台：优先使用强制平台（/antigravity 路由，中间件已设置 request.Context），否则要求 gemini/antigravity 分组
	// TK: platform allow — see gemini_v1beta_handler_tk_platform.go
	if !middleware.HasForcePlatform(c) {
		if !geminiV1BetaGroupPlatformAllowed(apiKey) {
			googleError(c, http.StatusBadRequest, "API key group cannot serve native Gemini requests")
			return
		}
	}

	modelName, action, err := parseGeminiModelAction(strings.TrimPrefix(c.Param("modelAction"), "/"))
	if err != nil {
		googleError(c, http.StatusNotFound, err.Error())
		return
	}
	if action == "countTokens" {
		ExcludeTerminalOutcome(c)
	}
	// URL 里的模型名最终会被拼进上游 /v1beta/models/{model}:{action}，
	// 先在入口校验片段合规性，见 service/upstream_path_guard.go。
	if !service.IsSafeGeminiModelPathSegment(modelName) {
		googleError(c, http.StatusBadRequest, "Invalid model in URL")
		return
	}
	if resolvedModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context()); ok && strings.TrimSpace(resolvedModel) != "" {
		modelName = strings.TrimSpace(resolvedModel)
	}

	stream := action == "streamGenerateContent"
	reqLog = reqLog.With(zap.String("model", modelName), zap.String("action", action), zap.Bool("stream", stream))

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		writeGoogleReadRequestBodyError(c, err)
		return
	}
	if len(body) == 0 {
		googleError(c, http.StatusBadRequest, "Request body is empty")
		return
	}
	// TK: canonical Gemini generateContent + WithProtocolRouting — see gemini_v1beta_handler_tk_execute.go
	if action != "countTokens" {
		if attachErr := h.tkAttachGeminiGenerateContentProtocolRouting(c, modelName, stream, body); attachErr != nil {
			googleError(c, http.StatusBadRequest, "Invalid request body")
			return
		}
	}

	setOpsRequestModelAndBody(c, modelName, stream, body)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(stream, false)))
	pricingCtx, pricingAt := service.WithGatewayTokenRequestPricing(c.Request.Context())
	c.Request = c.Request.WithContext(pricingCtx)

	// TK: pre-flight body-size guard (see gateway_handler_tk_body_guard.go).
	if reject, msg := TkEvalBodyGuard(reqLog, h.cfg.Gateway.UpstreamBodyGuards, domain.PlatformGemini, modelName, len(body)); reject {
		googleError(c, http.StatusRequestEntityTooLarge, msg)
		return
	}

	if decision := h.checkContentModeration(c, reqLog, apiKey, authSubject, service.ContentModerationProtocolGemini, modelName, body); decision != nil && decision.Blocked {
		googleError(c, contentModerationStatus(decision), decision.Message)
		return
	}
	if decision := h.checkSecurityAudit(c, reqLog, apiKey, authSubject, service.ContentModerationProtocolGemini, modelName, body); decision != nil && !decision.AllowNextStage {
		googleSecurityAuditError(c, decision)
		return
	}

	// 解析渠道级模型映射
	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, modelName)
	reqModel := modelName // 保存映射前的原始模型名
	if channelMapping.Mapped {
		modelName = channelMapping.MappedModel
	}

	// Get subscription (may be nil)
	subscription, _ := middleware.GetSubscriptionFromContext(c)

	// For Gemini native API, do not send Claude-style ping frames.
	geminiConcurrency := NewConcurrencyHelper(h.concurrencyHelper.concurrencyService, SSEPingFormatNone, 0)

	// 1) user concurrency slot
	streamStarted := false
	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}
	userReleaseFunc, err := geminiConcurrency.AcquireUserSlotWithWait(c, authSubject.UserID, authSubject.Concurrency, stream, &streamStarted)
	if err != nil {
		reqLog.Warn("gemini.user_slot_acquire_failed", zap.Error(err))
		googleError(c, http.StatusTooManyRequests, err.Error())
		return
	}
	// 确保请求取消时也会释放槽位，避免长连接被动中断造成泄漏
	userReleaseFunc = wrapReleaseOnDone(c.Request.Context(), userReleaseFunc)
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// 2) billing eligibility check (after wait)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("gemini.billing_eligibility_check_failed", zap.Error(err))
		status, _, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		googleError(c, status, message)
		return
	}

	// 3) select account (sticky session based on request body)
	// 优先使用 Gemini CLI 的会话标识（privileged-user-id + tmp 目录哈希）
	sessionHash := extractGeminiCLISessionHash(c, body)
	if sessionHash == "" {
		// Fallback: 使用通用的会话哈希生成逻辑（适用于其他客户端）
		parsedReq, _ := service.ParseGatewayRequest(service.NewRequestBodyRef(body), domain.PlatformGemini)
		if parsedReq != nil {
			TkPrepareParsedRequestSessionInputs(c, apiKey, parsedReq)
		}
		sessionHash = h.gatewayService.GenerateSessionHash(parsedReq)
	}
	sessionKey := sessionHash
	if sessionHash != "" {
		sessionKey = "gemini:" + sessionHash
	}

	// 查询粘性会话绑定的账号 ID（用于检测账号切换）
	var sessionBoundAccountID int64
	if sessionKey != "" {
		sessionBoundAccountID, _ = h.gatewayService.GetCachedSessionAccountID(c.Request.Context(), apiKey.GroupID, sessionKey)
		if sessionBoundAccountID > 0 {
			prefetchedGroupID := int64(0)
			if apiKey.GroupID != nil {
				prefetchedGroupID = *apiKey.GroupID
			}
			ctx := service.WithPrefetchedStickySession(c.Request.Context(), sessionBoundAccountID, prefetchedGroupID, h.metadataBridgeEnabled())
			c.Request = c.Request.WithContext(ctx)
		}
	}

	// === Gemini 内容摘要会话 Fallback 逻辑 ===
	// 当原有会话标识无效时（sessionBoundAccountID == 0），尝试基于内容摘要链匹配
	var geminiDigestChain string
	var geminiPrefixHash string
	var geminiSessionUUID string
	var matchedDigestChain string
	useDigestFallback := sessionBoundAccountID == 0

	if useDigestFallback {
		// 解析 Gemini 请求体
		var geminiReq antigravity.GeminiRequest
		if err := json.Unmarshal(body, &geminiReq); err == nil && len(geminiReq.Contents) > 0 {
			// 生成摘要链
			geminiDigestChain = service.BuildGeminiDigestChain(&geminiReq)
			if geminiDigestChain != "" {
				// 生成前缀 hash
				userAgent := c.GetHeader("User-Agent")
				clientIP := ip.GetClientIP(c)
				platform := ""
				if apiKey.Group != nil {
					platform = apiKey.Group.Platform
				}
				geminiPrefixHash = service.GenerateGeminiPrefixHash(
					authSubject.UserID,
					apiKey.ID,
					clientIP,
					userAgent,
					platform,
					modelName,
				)

				// 查找会话
				foundUUID, foundAccountID, foundMatchedChain, found := h.gatewayService.FindGeminiSession(
					c.Request.Context(),
					derefGroupID(apiKey.GroupID),
					geminiPrefixHash,
					geminiDigestChain,
				)
				if found {
					matchedDigestChain = foundMatchedChain
					sessionBoundAccountID = foundAccountID
					geminiSessionUUID = foundUUID
					reqLog.Info("gemini.digest_fallback_matched",
						zap.String("session_uuid_prefix", safeShortPrefix(foundUUID, 8)),
						zap.Int64("account_id", foundAccountID),
						zap.String("digest_chain", truncateDigestChain(geminiDigestChain)),
					)

					// 关键：如果原 sessionKey 为空，使用 prefixHash + uuid 作为 sessionKey
					// 这样 SelectAccountWithLoadAwareness 的粘性会话逻辑会优先使用匹配到的账号
					if sessionKey == "" {
						sessionKey = service.GenerateGeminiDigestSessionKey(geminiPrefixHash, foundUUID)
					}
					_ = h.gatewayService.BindStickySession(c.Request.Context(), apiKey.GroupID, sessionKey, foundAccountID)
				} else {
					// 生成新的会话 UUID
					geminiSessionUUID = uuid.New().String()
					// 为新会话也生成 sessionKey（用于后续请求的粘性会话）
					if sessionKey == "" {
						sessionKey = service.GenerateGeminiDigestSessionKey(geminiPrefixHash, geminiSessionUUID)
					}
				}
			}
		}
	}

	// 判断是否真的绑定了粘性会话：有 sessionKey 且已经绑定到某个账号
	hasBoundSession := sessionKey != "" && sessionBoundAccountID > 0
	cleanedForUnknownBinding := false
	cleanThoughtSignatures := false

	fs := NewFailoverState(h.maxAccountSwitchesGemini, hasBoundSession)
	if service.CandidateRequestFromContext(c.Request.Context()) == nil && apiKey.IsUniversal() && apiKey.Group != nil && apiKey.Group.Platform == service.PlatformNewAPI {
		ctx := service.WithNativeGeminiVertexAccountRequirement(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
	}

	// 单账号分组提前设置 SingleAccountRetry 标记，让 Service 层首次 503 就不设模型限流标记。
	// 避免单账号分组收到 503 (MODEL_CAPACITY_EXHAUSTED) 时设 29s 限流，导致后续请求连续快速失败。
	if h.gatewayService.IsSingleAntigravityAccountGroup(c.Request.Context(), apiKey.GroupID) {
		ctx := service.WithSingleAccountRetry(c.Request.Context(), true, h.metadataBridgeEnabled())
		c.Request = c.Request.WithContext(ctx)
	}

	for {
		selection, err := h.gatewayService.SelectAccountWithLoadAwareness(c.Request.Context(), apiKey.GroupID, sessionKey, modelName, fs.FailedAccountIDs, "", int64(0)) // Gemini 不使用会话限制
		if err != nil {
			if len(fs.FailedAccountIDs) == 0 {
				cls := classifyNoAccountErrorFromGin(c, h.gatewayService, apiKey, modelName, modelName, service.PlatformGemini)
				if !cls.ModelNotFound {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				}
				message := cls.Message
				if !cls.ModelNotFound {
					message = "No available Gemini accounts: " + err.Error()
				}
				googleError(c, cls.Status, message)
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
				h.handleGeminiFailoverExhausted(c, fs.LastFailoverErr)
				return
			}
		}
		account := selection.Account
		if service.CandidateRequestFromContext(c.Request.Context()) == nil && apiKey.IsUniversal() && apiKey.Group != nil && apiKey.Group.Platform == service.PlatformNewAPI && !account.IsNewAPIVertexServiceAccount() {
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			reqLog.Error("gemini.universal_vertex_account_rejected",
				zap.Int64("account_id", account.ID),
				zap.Int("channel_type", account.ChannelType),
			)
			googleError(c, http.StatusServiceUnavailable, "No available Gemini accounts")
			return
		}
		setOpsSelectedAccountFrom(c, account)

		// 检测账号切换：如果粘性会话绑定的账号与当前选择的账号不同，清除 thoughtSignature
		// 注意：Gemini 原生 API 的 thoughtSignature 与具体上游账号强相关；跨账号透传会导致 400。
		if sessionBoundAccountID > 0 && sessionBoundAccountID != account.ID {
			reqLog.Info("gemini.sticky_session_account_switched",
				zap.Int64("from_account_id", sessionBoundAccountID),
				zap.Int64("to_account_id", account.ID),
				zap.Bool("clean_thought_signature", true),
			)
			body = service.CleanGeminiNativeThoughtSignatures(body)
			cleanThoughtSignatures = true
			sessionBoundAccountID = account.ID
		} else if sessionKey != "" && sessionBoundAccountID == 0 && !cleanedForUnknownBinding && bytes.Contains(body, []byte(`"thoughtSignature"`)) {
			// 无缓存绑定但请求里已有 thoughtSignature：常见于缓存丢失/TTL 过期后，客户端继续携带旧签名。
			// 为避免第一次转发就 400，这里做一次确定性清理，让新账号重新生成签名链路。
			reqLog.Info("gemini.sticky_session_binding_missing",
				zap.Bool("clean_thought_signature", true),
			)
			body = service.CleanGeminiNativeThoughtSignatures(body)
			cleanThoughtSignatures = true
			cleanedForUnknownBinding = true
			sessionBoundAccountID = account.ID
		} else if sessionBoundAccountID == 0 {
			// 记录本次请求中首次选择到的账号，便于同一请求内 failover 时检测切换。
			sessionBoundAccountID = account.ID
		}

		// 4) account concurrency slot
		accountReleaseFunc := selection.ReleaseFunc
		if !selection.Acquired {
			if selection.WaitPlan == nil {
				markOpsRoutingCapacityLimited(c)
				googleError(c, tkNoAvailableAccounts(c), "No available accounts")
				return
			}
			accountWaitCounted := false
			canWait, err := geminiConcurrency.IncrementAccountWaitCount(c.Request.Context(), account.ID, selection.WaitPlan.MaxWaiting)
			if err != nil {
				reqLog.Warn("gemini.account_wait_counter_increment_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			} else if !canWait {
				reqLog.Info("gemini.account_wait_queue_full",
					zap.Int64("account_id", account.ID),
					zap.Int("max_waiting", selection.WaitPlan.MaxWaiting),
				)
				googleError(c, http.StatusTooManyRequests, "Too many pending requests, please retry later")
				return
			}
			if err == nil && canWait {
				accountWaitCounted = true
			}
			defer func() {
				if accountWaitCounted {
					geminiConcurrency.DecrementAccountWaitCount(c.Request.Context(), account.ID)
				}
			}()

			accountReleaseFunc, err = geminiConcurrency.AcquireAccountSlotWithWaitTimeout(
				c,
				account.ID,
				selection.WaitPlan.MaxConcurrency,
				selection.WaitPlan.Timeout,
				stream,
				&streamStarted,
			)
			if err != nil {
				reqLog.Warn("gemini.account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
				googleError(c, http.StatusTooManyRequests, err.Error())
				return
			}
			if accountWaitCounted {
				geminiConcurrency.DecrementAccountWaitCount(c.Request.Context(), account.ID)
				accountWaitCounted = false
			}
		}
		// 终检与准入后绑定使用选号结果携带的门（见 responses 同名注释）。
		admissionCtx := service.ContextWithSelectionProfitGate(c.Request.Context(), selection)
		latest, vetoed, reason := h.gatewayService.GatewayProfitControlVetoLatest(admissionCtx, account)
		if vetoed {
			if accountReleaseFunc != nil {
				accountReleaseFunc()
			}
			reqLog.Debug("gemini.account_slot_profit_vetoed", zap.Int64("account_id", account.ID), zap.String("reason", reason))
			if fs.RecordProfitVeto(account.ID) == FailoverExhausted {
				reqLog.Warn("gemini.profit_veto_attempts_exhausted", zap.Int("profit_veto_count", fs.ProfitVetoCount()))
				markOpsRoutingCapacityLimited(c)
				googleError(c, http.StatusServiceUnavailable, profitVetoExhaustedMessage)
				return
			}
			continue
		}
		account = latest
		selection.Account = latest
		// 等待路径保持既有 eager 绑定（无门时 helper 直接绑定）；调度器已抢槽
		// 的直达路径无门时由选号内部绑定，这里只在门下补准入后绑定。
		if selection.ProfitGateActive() || !selection.Acquired {
			if err := h.gatewayService.BindStickySessionAfterProfitAdmission(admissionCtx, apiKey.GroupID, sessionKey, account.ID); err != nil {
				reqLog.Warn("gemini.bind_sticky_session_after_profit_admission_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			}
		}
		// 账号槽位/等待计数需要在超时或断开时安全回收
		accountReleaseFunc = wrapReleaseOnDone(c.Request.Context(), accountReleaseFunc)

		// 5) forward through the immutable protocol plan for governed accounts.
		var result *service.ForwardResult
		requestCtx := c.Request.Context()
		if fs.SwitchCount > 0 {
			requestCtx = service.WithAccountSwitchCount(requestCtx, fs.SwitchCount, h.metadataBridgeEnabled())
		}
		sessionGroupID := derefGroupID(apiKey.GroupID)
		// TK: ProtocolExecutors wiring — see gemini_v1beta_handler_tk_execute.go
		result, err = h.executeGeminiV1BetaSelectedProtocol(
			c, requestCtx, selection, account, modelName, action, stream, hasBoundSession, sessionGroupID, sessionKey, cleanThoughtSignatures,
		)
		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}
		if err != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				failoverAction := fs.HandleFailoverError(c.Request.Context(), h.gatewayService, account.ID, account.Platform, account.GetPoolModeRetryCount(), failoverErr)
				switch failoverAction {
				case FailoverContinue:
					continue
				case FailoverExhausted:
					h.handleGeminiFailoverExhausted(c, fs.LastFailoverErr)
					return
				case FailoverCanceled:
					failoverClientGone(c)
					return
				}
			}
			// ForwardNative already wrote the response
			reqLog.Error("gemini.forward_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			// TK: passive availability failure tap (R-004 — extracts upstream HTTP status from UpstreamFailoverError)
			TkRecordFailureFromErr(h.gatewayService, c.Request.Context(), account.Platform, modelName, account.ID, err)
			return
		}

		setOpsForwardResultContext(c, result.UpstreamModel, reqModel)
		setOpsClaudeUsageContext(c, result.Usage)

		// 捕获请求信息（用于异步记录，避免在 goroutine 中访问 gin.Context）
		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)

		// 保存 Gemini 内容摘要会话（用于 Fallback 匹配）
		// TK: digest session save — see gemini_v1beta_handler_tk_session.go
		h.tkSaveGeminiDigestSession(
			c.Request.Context(),
			reqLog,
			derefGroupID(apiKey.GroupID),
			useDigestFallback,
			geminiDigestChain,
			geminiPrefixHash,
			geminiSessionUUID,
			matchedDigestChain,
			account.ID,
		)

		// 使用量记录通过有界 worker 池提交，避免请求热路径创建无界 goroutine。
		requestPayloadHash := service.HashUsageRequestPayload(body)
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
		// ForceCacheBilling 提前拍成标量，避免 worker 闭包保活 failover 状态里的响应体。
		forceCacheBilling := fs.ForceCacheBilling
		billingAPIKey, billingSubscription := snapshotCandidateBilling(c.Request.Context(), apiKey, subscription)
		usageFields := clientRequestedUsageFields(c, channelMapping, reqModel, result.UpstreamModel)
		quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
		sessionID := service.ExtractClientSessionID(c)
		// 长上下文阶梯由目录数据驱动，统一在计费路径内生效，入口无需声明。
		h.submitUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
			if err := h.gatewayService.RecordUsageWithLongContext(ctx, &service.RecordUsageLongContextInput{
				Result:                result,
				QuotaPlatform:         quotaPlatform,
				APIKey:                billingAPIKey,
				User:                  billingAPIKey.User,
				Account:               account,
				Subscription:          billingSubscription,
				PricingAt:             pricingAt,
				InboundEndpoint:       inboundEndpoint,
				UpstreamEndpoint:      upstreamEndpoint,
				UserAgent:             userAgent,
				IPAddress:             clientIP,
				RequestPayloadHash:    requestPayloadHash,
				LongContextThreshold:  200000, // Gemini 200K 阈值
				LongContextMultiplier: 2.0,    // 超出部分双倍计费
				ForceCacheBilling:     forceCacheBilling,
				APIKeyService:         h.apiKeyService,
				SessionID:             sessionID,
				ChannelUsageFields:    usageFields,
			}); err != nil {
				logger.L().With(
					zap.String("component", "handler.gemini_v1beta.models"),
					zap.Int64("user_id", authSubject.UserID),
					zap.Int64("api_key_id", apiKey.ID),
					zap.Any("group_id", apiKey.GroupID),
					zap.String("model", modelName),
					zap.Int64("account_id", account.ID),
				).Error("gemini.record_usage_failed", zap.Error(err))
			}
		})
		reqLog.Debug("gemini.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", fs.SwitchCount),
		)
		return
	}
}

func parseGeminiModelAction(rest string) (model string, action string, err error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", &pathParseError{"missing path"}
	}

	// Standard: {model}:{action}
	if i := strings.Index(rest, ":"); i > 0 && i < len(rest)-1 {
		return rest[:i], rest[i+1:], nil
	}

	// Fallback: {model}/{action}
	if i := strings.Index(rest, "/"); i > 0 && i < len(rest)-1 {
		return rest[:i], rest[i+1:], nil
	}

	return "", "", &pathParseError{"invalid model action path"}
}

func mapGeminiUpstreamError(statusCode int) (int, string) {
	switch statusCode {
	case 401:
		return http.StatusBadGateway, "Upstream authentication failed, please contact administrator"
	case 403:
		return http.StatusBadGateway, "Upstream access forbidden, please contact administrator"
	case 429:
		return http.StatusTooManyRequests, "Upstream rate limit exceeded, please retry later"
	case 529:
		return http.StatusServiceUnavailable, "Upstream service overloaded, please retry later"
	case 500, 502, 503, 504:
		return http.StatusBadGateway, "Upstream service temporarily unavailable"
	default:
		return http.StatusBadGateway, "Upstream request failed"
	}
}

type pathParseError struct{ msg string }

func (e *pathParseError) Error() string { return e.msg }

func googleError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  googleapi.HTTPStatusToGoogleStatus(status),
		},
	})
}

func writeUpstreamResponse(c *gin.Context, res *service.UpstreamHTTPResult) {
	if res == nil {
		googleError(c, http.StatusBadGateway, "Empty upstream response")
		return
	}
	for k, vv := range res.Headers {
		// Avoid overriding content-length and hop-by-hop headers.
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") || strings.EqualFold(k, "Connection") {
			continue
		}
		for _, v := range vv {
			c.Writer.Header().Add(k, v)
		}
	}
	contentType := res.Headers.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(res.StatusCode, contentType, res.Body)
}

func shouldFallbackGeminiModels(res *service.UpstreamHTTPResult) bool {
	if res == nil {
		return true
	}
	if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusForbidden {
		return false
	}
	if strings.Contains(strings.ToLower(res.Headers.Get("Www-Authenticate")), "insufficient_scope") {
		return true
	}
	if strings.Contains(strings.ToLower(string(res.Body)), "insufficient authentication scopes") {
		return true
	}
	if strings.Contains(strings.ToLower(string(res.Body)), "access_token_scope_insufficient") {
		return true
	}
	return false
}

func shouldFallbackGeminiModel(modelName string, res *service.UpstreamHTTPResult) bool {
	if shouldFallbackGeminiModels(res) {
		return true
	}
	if res == nil || res.StatusCode != http.StatusNotFound {
		return false
	}
	return gemini.HasFallbackModel(modelName)
}

// derefGroupID 安全解引用 *int64，nil 返回 0
func derefGroupID(groupID *int64) int64 {
	if groupID == nil {
		return 0
	}
	return *groupID
}

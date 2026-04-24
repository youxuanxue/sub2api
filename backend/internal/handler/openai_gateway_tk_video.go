package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// SetVideoTaskCache wires the registry post-construction. Mirrors the
// `SetSettingService` pattern used elsewhere to keep the upstream-shape
// NewOpenAIGatewayHandler signature stable across upstream merges
// (CLAUDE.md §5 — minimal injection point). Nil-safe: VideoSubmit and
// VideoFetch return 503 if the cache was never wired.
func (h *OpenAIGatewayHandler) SetVideoTaskCache(cache service.VideoTaskCache) {
	h.videoTaskCache = cache
}

// VideoSubmit handles POST /v1/video/generations and the OpenAI-compat alias
// POST /v1/videos. It is only available for OpenAI-compat platform groups
// (the route layer enforces this); within those, the account's channel_type
// determines which task adaptor is used (e.g. 45 → VolcEngine, 54 →
// DoubaoVideo). Returns a public task_id that the client can poll via
// VideoFetch.
//
// Video submit is synchronous from the gateway's perspective (no SSE / no
// streaming response) — we deliberately use errorResponse / JSON 4xx-5xx,
// NOT the streaming-aware wrappers used by chat / responses handlers.
func (h *OpenAIGatewayHandler) VideoSubmit(c *gin.Context) {
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
		"handler.openai_gateway.video_submit",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	if h.videoTaskCache == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Video task registry is not configured")
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
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
	promptResult := gjson.GetBytes(body, "prompt")
	if !promptResult.Exists() || promptResult.Type != gjson.String || strings.TrimSpace(promptResult.String()) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "prompt is required")
		return
	}
	reqLog = reqLog.With(zap.String("model", reqModel))

	setOpsRequestContext(c, reqModel, false, body)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(false, false)))

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		reqLog.Info("openai_video_submit.billing_eligibility_check_failed", zap.Error(err))
		status, code, message := billingErrorDetails(err)
		h.errorResponse(c, status, code, message)
		return
	}

	sessionHash := h.gatewayService.GenerateSessionHash(c, body)

	// Single account selection; video submit is one-shot (no streaming retries).
	selectionCtx, groupName := h.tkOpenAIChatSelectionCtx(c, apiKey, reqModel)
	selection, _, err := h.gatewayService.SelectAccountWithScheduler(
		selectionCtx,
		apiKey.GroupID,
		"",
		sessionHash,
		reqModel,
		map[int64]struct{}{},
		service.OpenAIUpstreamTransportAny,
	)
	if err != nil || selection == nil || selection.Account == nil {
		reqLog.Warn("openai_video_submit.account_select_failed", zap.Error(err))
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable")
		return
	}
	account := selection.Account
	if !bridge.IsVideoSupportedChannelType(account.ChannelType) {
		// channel_type=0 (incomplete account) and channel_type with no task
		// adaptor (e.g. plain OpenAI account asked to do video) collapse into
		// the same user-facing error: this group is not configured for video.
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Selected account's channel_type does not support video generation")
		return
	}
	setOpsSelectedAccount(c, account.ID, account.Platform)
	openAIMarkAffinitySelected(c, groupName, account.ID)

	service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
	forwardStart := time.Now()

	// Generate the public task id BEFORE dispatch so the bridge can stamp it
	// onto the wire response (the new-api task adaptor's DoResponse writes
	// the OpenAI-Video JSON to gin.Context inside the bridge call). The
	// handler does NOT write a second JSON afterwards — that would corrupt
	// the response stream.
	publicTaskID := generateVideoTaskID()

	TkSetBridgeGinAuth(c, subject.UserID, groupName)
	outcome, err := h.gatewayService.ForwardAsVideoSubmitDispatched(c.Request.Context(), c, account, publicTaskID, body)
	forwardDurationMs := time.Since(forwardStart).Milliseconds()
	service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, forwardDurationMs)

	if err != nil {
		if TkTryWriteNewAPIRelayErrorJSON(c, err, false, 0) {
			reqLog.Warn("openai_video_submit.forward_failed", zap.Error(err))
			return
		}
		h.errorResponse(c, http.StatusBadGateway, "api_error", "Video submit failed")
		return
	}

	groupID := int64(0)
	if apiKey.GroupID != nil {
		groupID = *apiKey.GroupID
	}
	rec := &service.VideoTaskRecord{
		PublicTaskID:   publicTaskID,
		UpstreamTaskID: outcome.UpstreamTaskID,
		AccountID:      account.ID,
		UserID:         subject.UserID,
		GroupID:        groupID,
		APIKeyID:       apiKey.ID,
		ChannelType:    account.ChannelType,
		Platform:       account.Platform,
		// BaseURL + APIKey snapshot the upstream routing the bridge used at
		// submit time. We persist the bridge's resolved values (not a fresh
		// account read) because credentials may rotate before the user polls,
		// and a fetch must hit the same upstream endpoint that accepted the
		// submit. The bridge already centralises the resolution chain
		// (credentials.api_key → openai_api_key fallback, base_url platform
		// guard) — duplicating it here would be a DRY violation and a source
		// of subtle drift.
		BaseURL:       outcome.BaseURL,
		APIKey:        outcome.APIKey,
		OriginModel:   outcome.OriginModel,
		UpstreamModel: outcome.UpstreamModel,
		CreatedAt:     time.Now(),
	}
	if err := h.videoTaskCache.Save(c.Request.Context(), rec); err != nil {
		// At this point the bridge has already written the success body
		// (with publicTaskID) to the client. Failing the registry save
		// would orphan the upstream task — log and continue so the user
		// at least gets a usable task_id back.
		reqLog.Error("openai_video_submit.registry_save_failed",
			zap.String("public_task_id", publicTaskID),
			zap.String("upstream_task_id", outcome.UpstreamTaskID),
			zap.Error(err),
		)
	}

	openAIRecordAffinitySuccess(c, account.ID)

	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	h.submitUsageRecordTask(func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result: &service.OpenAIForwardResult{
				Model:         outcome.OriginModel,
				UpstreamModel: outcome.UpstreamModel,
				Stream:        false,
				Duration:      outcome.Duration,
			},
			APIKey:           apiKey,
			User:             apiKey.User,
			Account:          account,
			Subscription:     subscription,
			InboundEndpoint:  GetInboundEndpoint(c),
			UpstreamEndpoint: GetUpstreamEndpoint(c, account.Platform),
			UserAgent:        userAgent,
			IPAddress:        clientIP,
			APIKeyService:    h.apiKeyService,
		}); err != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.video_submit"),
				zap.Int64("user_id", subject.UserID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Any("group_id", apiKey.GroupID),
				zap.String("model", reqModel),
				zap.Int64("account_id", account.ID),
			).Error("openai_video_submit.record_usage_failed", zap.Error(err))
		}
	})
	// NOTE: no c.JSON here — the bridge already wrote the OpenAI-Video
	// success body (with publicTaskID stamped) inside DispatchVideoSubmit.
}

// VideoFetch handles GET /v1/video/generations/:task_id and the OpenAI-compat
// alias GET /v1/videos/:task_id. The task_id parameter is our public task id;
// we look up the registry, verify ownership, and replay the FetchTask call to
// the upstream account that originally accepted the submit. When the upstream
// reports a terminal status we delete the registry entry to bound storage;
// clients that poll after that will see 404.
//
// Authorization model:
//   - Route layer (tkOpenAICompatVideoFetchHandler) gates on the caller's
//     group.platform being OpenAI-compatible (openai or newapi). Anthropic /
//     Gemini / Antigravity callers never reach this handler.
//   - Handler enforces ownership: record.UserID must equal the caller's
//     subject.UserID. A leaked or guessed task_id from another user surfaces
//     as 404 (deliberately indistinguishable from "expired" — we do not leak
//     existence). This is the same invariant the rest of the gateway uses
//     for per-user resources.
func (h *OpenAIGatewayHandler) VideoFetch(c *gin.Context) {
	publicTaskID := strings.TrimSpace(c.Param("task_id"))
	if publicTaskID == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "task_id is required")
		return
	}
	if h.videoTaskCache == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Video task registry is not configured")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}

	// Both lookup-miss and cross-user-mismatch surface as 404 (not 403)
	// so we never confirm a task_id's existence to a non-owner. Merging
	// the two branches keeps the response shape identical from a probe's
	// perspective — the only signal a non-owner can extract is "doesn't
	// exist for me", which is also what an owner sees post-expiry.
	rec, ok := h.videoTaskCache.Lookup(c.Request.Context(), publicTaskID)
	if !ok || rec.UserID != subject.UserID {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "video task not found or expired")
		return
	}

	in := bridge.VideoFetchInput{
		UpstreamTaskID: rec.UpstreamTaskID,
		ChannelType:    rec.ChannelType,
		BaseURL:        rec.BaseURL,
		APIKey:         rec.APIKey,
	}
	out, err := h.gatewayService.ForwardAsVideoFetchDispatched(c.Request.Context(), c, in)
	if err != nil {
		if TkTryWriteNewAPIRelayErrorJSON(c, err, false, 0) {
			return
		}
		h.errorResponse(c, http.StatusBadGateway, "api_error", "Video fetch failed")
		return
	}

	// Pass the upstream JSON through untouched so volcengine / doubao SDK
	// clients see exactly the body shape new-api would have returned for the
	// same channel type. We deliberately do NOT wrap in {code,success,data}
	// at this layer — the upstream already does so for the OpenAI-Video API
	// shape that `/v1/videos/:task_id` clients rely on.
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	if len(out.RawResponse) == 0 {
		_, _ = c.Writer.WriteString("{}")
	} else {
		_, _ = c.Writer.Write(out.RawResponse)
	}

	// On terminal status we drop the entry to bound storage; clients that
	// need the URL must have already consumed the response body above.
	switch strings.ToLower(out.Status) {
	case "success", "succeeded", "failure", "failed":
		h.videoTaskCache.Delete(c.Request.Context(), publicTaskID)
	}
}

// generateVideoTaskID — `vt_` prefix mirrors OpenAI's `vid_` / `task_`
// conventions and makes the id obviously a TokenKey artifact (not the
// upstream's id) when surfaced in logs and dashboards.
func generateVideoTaskID() string {
	return "vt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

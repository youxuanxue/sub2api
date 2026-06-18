package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine"
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
	// Video-INPUT (continuation / reference video) is rejected until it is
	// priced: upstream bills (input+output) duration for video-in requests,
	// so the per-output-second price would under-charge 1.6–2.4x. First-frame
	// image input stays allowed. Re-pricing must land together with lifting
	// this guard — do not add a bypass toggle without it.
	if videoSubmitHasVideoInput(body) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error",
			"Video input (video_url content) is not supported: video-input generation is not yet priced on this gateway. Remove the video_url content part; first-frame image input (image_url) is supported.")
		return
	}
	// Unpriced media is not served (pre-spend 400 instead of post-spend $0 +
	// P0 alert — one video task is real upstream money); see
	// openai_gateway_service_tk_media_unpriced_guard.go for the policy.
	if h.gatewayService.TkVideoModelUnpriced(reqModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error",
			service.TkUnpricedMediaModelMessage(reqModel, "video"))
		return
	}
	reqLog = reqLog.With(zap.String("model", reqModel))

	setOpsRequestModelAndBody(c, reqModel, false, body)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(false, false)))

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	// TK: pre-flight balance hold (concurrent-overdraft fix; see
	// openai_gateway_handler_tk_hold.go). Video reserves the exact submit-time
	// cost (same request-derived seconds the billing path uses); refund
	// ownership is handed to the usage-record task at submit time. Balance
	// users only.
	hold, holdReject := h.tkApplyVideoHold(c, apiKey, reqModel, videoRequestedSeconds(body))
	if holdReject {
		h.errorResponse(c, http.StatusForbidden, "insufficient_balance", tkInsufficientBalanceForHoldMsg)
		return
	}
	defer hold.ReleaseUnlessSettling()

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("openai_video_submit.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, _ := billingErrorDetails(err)
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
		false,
	)
	if err != nil || selection == nil || selection.Account == nil {
		reqLog.Warn("openai_video_submit.account_select_failed", zap.Error(err))
		if err == nil {
			// Scheduler returned no usable selection without an error → empty pool.
			markOpsRoutingCapacityLimited(c)
			h.errorResponse(c, tkNoAvailableAccounts(c), "api_error", "No available accounts")
			return
		}
		markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
		// Empty pool fast-fails 429 (#575 parity); other scheduler errors stay 503.
		tkStatus, tkType, tkMsg := tkSelectFailureStatusMessage(c, err, reqModel)
		h.errorResponse(c, tkStatus, tkType, tkMsg)
		return
	}
	account := selection.Account
	if !engine.IsVideoSupportedChannelType(account.ChannelType) {
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
	// Resolved once and stamped on BOTH the registry record and the usage
	// record below, so the terminal-failure refund can find the billed
	// usage_logs row by request_id in every resolution branch (ctx-derived
	// or generated).
	billingRequestID := service.TkResolveUsageBillingRequestID(c.Request.Context())
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
		BaseURL:          outcome.BaseURL,
		APIKey:           outcome.APIKey,
		OriginModel:      outcome.OriginModel,
		UpstreamModel:    outcome.UpstreamModel,
		BillingRequestID: billingRequestID,
		CreatedAt:        time.Now(),
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

	setOpsForwardResultContext(c, outcome.UpstreamModel, reqModel)
	openAIRecordAffinitySuccess(c, account.ID)
	setOpsOpenAIUsageContext(c, service.OpenAIUsage{})
	setOpsForwardResultContext(c, outcome.UpstreamModel, reqModel)

	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	// Per-second video billing (veo etc.): we record at submit using the requested
	// duration (default 8s) rather than at fetch — the submit path already holds the
	// full billing context (apiKey/user/account/subscription), and 8s is veo's
	// conservative max so we never under-charge when the field is omitted.
	videoSeconds := videoRequestedSeconds(body)
	tkHoldRequestID := hold.HandOffToSettlement()
	h.submitUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result: &service.OpenAIForwardResult{
				Model:                outcome.OriginModel,
				UpstreamModel:        outcome.UpstreamModel,
				RequestID:            billingRequestID,
				Stream:               false,
				Duration:             outcome.Duration,
				VideoDurationSeconds: &videoSeconds,
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
			TkHoldRequestID:  tkHoldRequestID,
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

	// Terminal handling. We delete the registry entry ONLY on terminal FAILURE
	// (paired with the refund). Terminal SUCCESS is deliberately KEPT until its
	// TTL: a Veo success body is a 10–20 MB inline base64 clip that takes ~30s to
	// stream, and a client whose fetch is aborted mid-download (slow link, tab
	// switch, an over-tight client timeout) must be able to RE-FETCH it. Deleting
	// on success made that retry 404, which the Studio then rendered as a false
	// "failed — refunded" card for a video that actually generated and was billed.
	// Storage stays bounded by the record TTL either way.
	if terminal, failed := videoTerminalOutcome(out.Status); terminal && failed {
		h.videoTaskCache.Delete(c.Request.Context(), publicTaskID)
		// The user paid at submit for a video that never materialized — reverse
		// the charge. Idempotency lives in the refund itself (usage_billing_dedup
		// keyed by the public task id), so a concurrent poll racing this Delete
		// cannot double-refund. Clients that never poll a failed task are never
		// refunded (registry record expires with its TTL); openai_video_refund.*
		// logs are the audit trail.
		h.scheduleVideoRefundAttempt(c.Request.Context(), rec, 0)
	}
}

// videoRefund* bound the terminal-failure refund re-attempt. The submit-time
// billed row is written by the same async usage-record worker pool with no
// ordering guarantee, so a fast terminal poll can run before it lands. Each
// worker task has only a few seconds of ctx budget, so instead of blocking a
// worker we re-attempt across fresh tasks spaced by a timer, up to a bound.
const (
	videoRefundMaxAttempts = 6
	videoRefundRetryDelay  = 5 * time.Second
)

// shouldRetryVideoRefund reports whether a refund attempt that returned
// `outcome` on 0-based `attempt` should be re-scheduled. Pure so the bound is
// unit-testable: only VideoRefundOriginPending (billed row not landed yet) is
// retryable, capped at videoRefundMaxAttempts total attempts.
func shouldRetryVideoRefund(outcome service.VideoRefundOutcome, attempt int) bool {
	return outcome == service.VideoRefundOriginPending && attempt+1 < videoRefundMaxAttempts
}

// scheduleVideoRefundAttempt runs the terminal-failure refund on the usage-record
// worker pool and, when the submit-time billed row has not landed yet
// (VideoRefundOriginPending), re-schedules a later attempt via a timer — NOT a
// blocked worker — so the bounded per-task ctx budget does not cap the total race
// window. Bounded by videoRefundMaxAttempts; idempotency (usage_billing_dedup
// keyed by the public task id) makes overlapping attempts safe. When the bound is
// exhausted and the row still has not landed (e.g. the submit-time record task was
// dropped under load), it leaves an Error-level reconciliation trail.
//
// Dispatched as MANDATORY (sync fallback on pool-drop): the non-mandatory path
// silently drops under queue pressure, which for a refund is unrecoverable money
// loss with no audit trail — the dropped task never runs, so neither the retry
// timer nor the reconciliation Error log fires. Mandatory guarantees the attempt
// executes (matching how image-generation usage records are recorded).
func (h *OpenAIGatewayHandler) scheduleVideoRefundAttempt(parent context.Context, rec *service.VideoTaskRecord, attempt int) {
	h.submitMandatoryUsageRecordTask(parent, func(ctx context.Context) {
		outcome := h.gatewayService.RefundVideoUsageOnFailure(ctx, rec)
		if shouldRetryVideoRefund(outcome, attempt) {
			time.AfterFunc(videoRefundRetryDelay, func() {
				h.scheduleVideoRefundAttempt(parent, rec, attempt+1)
			})
			return
		}
		if outcome == service.VideoRefundOriginPending {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.video_refund"),
				zap.String("public_task_id", rec.PublicTaskID),
				zap.Int64("user_id", rec.UserID),
				zap.Int("attempts", attempt+1),
			).Error("openai_video_refund.abandoned_origin_never_landed")
		}
	})
}

// videoTerminalOutcome classifies an upstream task status string for the
// fetch path: terminal=true drops the registry entry; failed=true triggers
// the submit-charge refund. The status reaching here is string(TaskInfo.Status)
// — i.e. new-api's model.TaskStatus* constants ("SUCCESS"/"FAILURE"/...) plus
// the lowercase OpenAI-video spellings some adaptors emit. The contract test
// in openai_gateway_tk_video_contract_test.go locks this mapping against the
// pinned new-api constants so a .new-api-ref bump that renames them goes red
// here instead of silently disabling refunds.
func videoTerminalOutcome(status string) (terminal bool, failed bool) {
	switch strings.ToLower(status) {
	case "failure", "failed":
		return true, true
	case "success", "succeeded":
		return true, false
	}
	return false, false
}

// generateVideoTaskID — `vt_` prefix mirrors OpenAI's `vid_` / `task_`
// conventions and makes the id obviously a TokenKey artifact (not the
// upstream's id) when surfaced in logs and dashboards.
func generateVideoTaskID() string {
	return "vt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// videoRequestedSeconds extracts the requested video duration (seconds) from an
// OpenAI-compat video request body, trying the common field spellings. Falls back to a
// conservative 8s (veo's max) so per-second billing never under-charges when the client
// omits the field; clamped to a sane [1,60] range.
func videoRequestedSeconds(body []byte) int64 {
	for _, key := range []string{"seconds", "duration_seconds", "duration"} {
		r := gjson.GetBytes(body, key)
		if !r.Exists() {
			continue
		}
		// Float() parses both JSON numbers and numeric strings ("6"); round to nearest
		// so fractional durations bill up rather than truncate down.
		if f := r.Float(); f > 0 {
			n := int64(f + 0.5)
			if n > 60 {
				n = 60
			}
			return n
		}
	}
	return 8
}

// videoSubmitHasVideoInput reports whether a video submit body carries a
// VIDEO input content part (continuation / reference video). Upstream bills
// (input+output) duration for those, so TokenKey's per-output-second price
// would under-charge — the submit handler rejects them until they are priced.
// First-frame image input (image_url parts) must pass.
//
// Carrier shapes (new-api TaskSubmitReq semantics): on the JSON path the real
// carrier is "metadata.content" — sent either as a nested object OR as a
// JSON-ENCODED STRING (TaskSubmitReq.UnmarshalJSON accepts both; the doubao
// adaptor reads metadata["content"] after either form). Top-level "content"
// is only routed into Metadata on the multipart path; on JSON it is dropped
// upstream — still rejected here so a client's video_url is never silently
// ignored while they are billed for a text-to-video run.
func videoSubmitHasVideoInput(body []byte) bool {
	candidates := []gjson.Result{
		gjson.GetBytes(body, "content"),
		gjson.GetBytes(body, "metadata.content"),
	}
	if md := gjson.GetBytes(body, "metadata"); md.Type == gjson.String {
		candidates = append(candidates, gjson.Parse(md.String()).Get("content"))
	}
	for _, parts := range candidates {
		if !parts.IsArray() {
			continue
		}
		hasVideo := false
		parts.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "video_url" || part.Get("video_url").Exists() {
				hasVideo = true
				return false
			}
			return true
		})
		if hasVideo {
			return true
		}
	}
	return false
}

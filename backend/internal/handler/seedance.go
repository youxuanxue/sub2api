package handler

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// SeedanceTasks exposes Ark's native asynchronous video task protocol.
func (h *OpenAIGatewayHandler) SeedanceTasks(c *gin.Context) {
	if c.Request.Method == http.MethodPost && c.GetHeader("Content-Type") != "" {
		mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || mediaType != "application/json" {
			h.errorResponse(c, http.StatusUnsupportedMediaType, "invalid_request_error", "Seedance requires application/json")
			return
		}
	}
	if c.Request.Method == http.MethodGet && strings.HasPrefix(c.Param("task_id"), "vt_") {
		h.VideoFetch(c)
		return
	}
	key, ok := middleware.GetAPIKeyFromContext(c)
	platform := ""
	if ok && key.Group != nil {
		platform = key.Group.Platform
	}
	// Video selection admits newapi task-adaptor accounts across the caller's
	// authorized groups. It must run before the OpenAI seedance-capability
	// gate, which rejects a universal key that has not had a billing group
	// attached yet.
	if service.SeedanceContentsUsesVideoSelection(c.Request.Context(), platform) {
		switch c.Request.Method {
		case http.MethodGet:
			h.VideoFetch(c)
		case http.MethodPost:
			raw, err := io.ReadAll(c.Request.Body)
			if err != nil {
				h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
				return
			}
			rewritten, err := service.SeedanceContentsAsVideoSubmitBody(raw)
			if err != nil {
				h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(rewritten))
			c.Request.ContentLength = int64(len(rewritten))
			h.VideoSubmit(c)
		default:
			h.errorResponse(c, http.StatusMethodNotAllowed, "invalid_request_error", "Seedance task deletion is not supported for this group")
		}
		return
	}
	if !ok || key.Group == nil || !service.SeedanceGroupPlatformAllowed(platform) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "Seedance requires an OpenAI or composite group")
		return
	}
	endpoint := service.SeedanceEndpointCreate
	setActualUpstreamEndpoint(c, EndpointSeedanceTasks)
	taskID := ""
	if c.Request.Method != http.MethodPost {
		taskID = service.SeedanceTaskKey(c.Param("task_id"))
		endpoint = service.SeedanceEndpointStatus
		if c.Request.Method == http.MethodDelete {
			endpoint = service.SeedanceEndpointDelete
		}
	}
	h.handleGrokMedia(c, endpoint, taskID)
}

// Ark reports actual completion tokens. Never infer tokens from duration or use
// Grok's per-second video tariff. Repeated polls share the durable task dedup key.
func prepareSeedanceCompletionBilling(ctx context.Context, h *OpenAIGatewayHandler, key *service.APIKey, subject middleware.AuthSubject, taskID string, result *service.OpenAIForwardResult) *service.OpenAIForwardResult {
	if result == nil || result.Usage.OutputTokens <= 0 {
		return nil
	}
	pending, err := h.gatewayService.LoadGrokVideoPendingBilling(ctx, taskID, subject.UserID, key.ID)
	if err != nil || pending == nil {
		return nil
	}
	claimed, err := h.gatewayService.ClaimGrokVideoBilling(ctx, taskID, subject.UserID, key.ID)
	if err != nil || !claimed {
		return nil
	}
	merged := *result
	merged.Model = pending.Model
	merged.BillingModel = firstNonEmptyString(pending.BillingModel, pending.Model)
	merged.UpstreamModel = firstNonEmptyString(pending.UpstreamModel, result.UpstreamModel)
	merged.RequestID = service.StableGrokVideoBillingRequestID(taskID)
	merged.ResponseID = taskID
	merged.Duration = service.GrokVideoE2EDuration(pending.CreatedAt, time.Now())
	return &merged
}

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// CodexModels serves the Codex models manifest for Codex clients.
//
// Codex CLI and the Codex desktop app refresh their model picker from
// GET {base_url}/models?client_version=... (custom provider mode) or
// GET /backend-api/codex/models (chatgpt_base_url mode). Both routes land
// here. ChatGPT manifests are proxied verbatim; custom API key manifests receive
// provider-compatibility normalization and use a short-lived, asynchronously
// revalidated cache to tolerate canceled client requests.
func (h *OpenAIGatewayHandler) CodexModels(c *gin.Context) {
	if c.Request.Context().Err() != nil {
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		h.errorResponse(c, http.StatusUnauthorized, "invalid_request_error", "Invalid API key")
		return
	}
	apiKey, allowedModelIDs, err := h.resolveCodexDiscoveryAPIKey(c.Request.Context(), apiKey)
	if err != nil {
		status := http.StatusInternalServerError
		message := "Codex model discovery unavailable"
		if errors.Is(err, service.ErrUniversalNoEntitledGroup) {
			status = http.StatusServiceUnavailable
			message = "No available OpenAI group for Codex"
		}
		h.errorResponse(c, status, "upstream_error", message)
		return
	}
	if allowedModelIDs != nil && len(allowedModelIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"models": []any{}})
		return
	}
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	if apiKey.Group == nil {
		h.errorResponse(c, http.StatusUnauthorized, "invalid_request_error", "API key group is required")
		return
	}
	if apiKey.Group.Platform != service.PlatformOpenAI && apiKey.Group.Platform != service.PlatformComposite {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Codex models manifest is only available for OpenAI and Composite groups")
		return
	}

	maxAccountSwitches := h.maxAccountSwitches
	if maxAccountSwitches <= 0 {
		maxAccountSwitches = 3
	}
	failedAccountIDs := make(map[int64]struct{})
	switchCount := 0
	var lastUpstreamErr error

	for {
		account, err := h.gatewayService.SelectAccountForModelWithExclusions(c.Request.Context(), apiKey.GroupID, "", "", failedAccountIDs)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			if lastUpstreamErr != nil {
				h.errorResponse(c, infraerrors.Code(lastUpstreamErr), "upstream_error", infraerrors.Message(lastUpstreamErr))
				return
			}
			h.errorResponse(c, http.StatusServiceUnavailable, "upstream_error", "No available OpenAI accounts")
			return
		}
		// 让 ops 错误日志携带实际选中的上游账号，便于定位失效账号（#4544）。
		setOpsSelectedAccountFrom(c, account)

		ifNoneMatch := c.GetHeader("If-None-Match")
		if allowedModelIDs != nil {
			ifNoneMatch = ""
		}
		manifest, err := h.gatewayService.FetchCodexModelsManifest(c.Request.Context(), account, c.Query("client_version"), ifNoneMatch)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			if service.IsRetryableCodexModelsManifestError(err) && switchCount < maxAccountSwitches {
				failedAccountIDs[account.ID] = struct{}{}
				switchCount++
				lastUpstreamErr = err
				continue
			}
			h.errorResponse(c, infraerrors.Code(err), "upstream_error", infraerrors.Message(err))
			return
		}
		if c.Request.Context().Err() != nil {
			return
		}

		if allowedModelIDs == nil && manifest.ETag != "" {
			c.Header("ETag", manifest.ETag)
		}
		if manifest.NotModified {
			c.Status(http.StatusNotModified)
			return
		}
		body := manifest.Body
		if allowedModelIDs != nil {
			body, err = filterCodexModelsManifest(body, allowedModelIDs)
			if err != nil {
				h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Codex models manifest could not be filtered")
				return
			}
		}
		c.Data(http.StatusOK, "application/json", body)
		return
	}
}

func (h *OpenAIGatewayHandler) resolveCodexDiscoveryAPIKey(ctx context.Context, apiKey *service.APIKey) (*service.APIKey, map[string]struct{}, error) {
	if apiKey == nil || !apiKey.IsUniversal() || apiKey.Group != nil {
		return apiKey, nil, nil
	}
	if h == nil || h.tkCapabilities == nil {
		return nil, nil, service.ErrUniversalCapabilityUnavailable
	}
	capabilities, err := h.tkCapabilities.List(ctx, apiKey, service.UniversalProtocolCodex)
	if err != nil {
		return nil, nil, err
	}
	allowedModelIDs := make(map[string]struct{}, len(capabilities))
	var selectedGroup *service.Group
	for i := range capabilities {
		if capabilities[i].ID != "" {
			allowedModelIDs[capabilities[i].ID] = struct{}{}
		}
		for _, route := range capabilities[i].Routes {
			if route.Protocol != service.UniversalProtocolCodex || route.Group.Platform != service.PlatformOpenAI {
				continue
			}
			if selectedGroup != nil {
				continue
			}
			selectedGroup = &service.Group{
				ID:       route.Group.ID,
				Name:     route.Group.Name,
				Platform: route.Group.Platform,
				Status:   service.StatusActive,
			}
		}
	}
	if selectedGroup == nil {
		return apiKey, allowedModelIDs, nil
	}
	return cloneAPIKeyWithGroup(apiKey, selectedGroup), allowedModelIDs, nil
}

func filterCodexModelsManifest(body []byte, allowedModelIDs map[string]struct{}) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	var models []json.RawMessage
	if err := json.Unmarshal(envelope["models"], &models); err != nil {
		return nil, fmt.Errorf("decode manifest models: %w", err)
	}
	filtered := make([]json.RawMessage, 0, len(models))
	for _, model := range models {
		var identity struct {
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(model, &identity); err != nil {
			continue
		}
		if _, ok := allowedModelIDs[identity.Slug]; ok {
			filtered = append(filtered, model)
		}
	}
	encodedModels, err := json.Marshal(filtered)
	if err != nil {
		return nil, fmt.Errorf("encode manifest models: %w", err)
	}
	envelope["models"] = encodedModels
	filteredBody, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return filteredBody, nil
}

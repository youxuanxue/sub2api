package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// CodexModels serves the Codex models manifest for Codex clients.
//
// Codex CLI and the Codex desktop app refresh their model picker from
// GET {base_url}/models?client_version=... (custom provider mode) or
// GET /backend-api/codex/models (chatgpt_base_url mode). Both routes land
// here. Pinned discovery takes precedence over local account model mappings;
// when disabled, groups with explicit mappings are generated locally;
// otherwise ChatGPT manifests are proxied verbatim and custom API key manifests
// receive provider-compatibility normalization plus short-lived caching.
func (h *OpenAIGatewayHandler) CodexModels(c *gin.Context) {
	if c.Request.Context().Err() != nil {
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		h.errorResponse(c, http.StatusUnauthorized, "invalid_request_error", "Invalid API key")
		return
	}
	discoveryAPIKeyID := apiKey.ID
	var allowedModelIDs map[string]struct{}
	var candidateAccounts []service.Account
	var err error
	source, candidateMode := h.tkCapabilities.(candidateCodexDiscoverySource)
	candidateMode = candidateMode && source.CandidateSchedulingEnabled()
	if candidateMode {
		var capabilities []service.UniversalCapability
		capabilities, candidateAccounts, err = source.DiscoverCandidates(c.Request.Context(), apiKey, service.UniversalProtocolCodex)
		ids := directCustomCapabilityIDs(apiKey, capabilityModelIDs(capabilities, service.UniversalModalityChat))
		allowedModelIDs = make(map[string]struct{}, len(ids))
		for _, id := range ids {
			allowedModelIDs[id] = struct{}{}
		}
	} else {
		apiKey, allowedModelIDs, err = h.resolveCodexDiscoveryAPIKey(c.Request.Context(), apiKey)
	}
	if err != nil {
		status := http.StatusInternalServerError
		message := "Codex model discovery unavailable"
		if errors.Is(err, service.ErrUniversalNoEntitledGroup) {
			status = http.StatusServiceUnavailable
			message = "No available OpenAI group for Codex"
		}
		service.SetOpsUpstreamError(c, 0, message, err.Error())
		requestLogger(c, "handler.openai_gateway.codex_models",
			zap.Int64("api_key_id", discoveryAPIKeyID),
			zap.String("stage", "capability_resolution"),
		).Error("codex.model_discovery_failed", zap.Error(err))
		h.errorResponse(c, status, "upstream_error", message)
		return
	}
	if allowedModelIDs != nil && len(allowedModelIDs) == 0 {
		c.JSON(http.StatusOK, gin.H{"models": []any{}})
		return
	}
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	if !candidateMode && apiKey.Group == nil {
		h.errorResponse(c, http.StatusUnauthorized, "invalid_request_error", "API key group is required")
		return
	}
	if !candidateMode && apiKey.Group.Platform != service.PlatformOpenAI && apiKey.Group.Platform != service.PlatformComposite {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Codex models manifest is only available for OpenAI and Composite groups")
		return
	}

	// Validate client caches only after all local authorization projections.
	ifNoneMatch := ""
	// 固定账号分支：开启后只用选定账号拉取 manifest，不经过调度器；
	// 全部不可用/全部失败时按 FallbackToScheduler 决定回退调度器或返回错误。
	if !apiKey.IsUniversal() && apiKey.Group != nil && apiKey.Group.Platform == service.PlatformOpenAI &&
		apiKey.Group.CodexModelsManifestConfig.Enabled {
		pinnedManifest, pinnedAccount, pinnedErr := h.gatewayService.FetchPinnedCodexModelsManifest(
			c.Request.Context(),
			apiKey.Group,
			c.Query("client_version"),
		)
		if pinnedErr != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			if !apiKey.Group.CodexModelsManifestConfig.FallbackToScheduler {
				if errors.Is(pinnedErr, service.ErrNoPinnedCodexModelsAccounts) {
					h.errorResponse(c, http.StatusServiceUnavailable, "upstream_error", "No available pinned OpenAI accounts")
					return
				}
				h.errorResponse(c, infraerrors.Code(pinnedErr), "upstream_error", infraerrors.Message(pinnedErr))
				return
			}
			// 回退开启：跌入下方调度器循环。
		} else {
			// 让 ops 错误日志携带实际拉取成功的首个固定账号。
			setOpsSelectedAccountFrom(c, pinnedAccount)
			if err := h.gatewayService.MergeGroupConfiguredCodexModels(c.Request.Context(), apiKey.Group, pinnedManifest, ifNoneMatch); err != nil {
				h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to build Codex models manifest")
				return
			}
			if c.Request.Context().Err() != nil {
				return
			}
			h.writeProjectedCodexModelsResponse(c, pinnedManifest, allowedModelIDs, candidateMode)
			return
		}
	}

	if !candidateMode && apiKey.Group != nil && !apiKey.Group.CodexModelsManifestConfig.Enabled {
		configuredManifest, configured, err := h.gatewayService.BuildGroupConfiguredCodexModelsManifest(
			c.Request.Context(),
			apiKey.Group,
			ifNoneMatch,
		)
		if err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to build Codex models manifest")
			return
		}
		if configured {
			h.writeProjectedCodexModelsResponse(c, configuredManifest, allowedModelIDs, candidateMode)
			return
		}
	}

	maxAccountSwitches := h.maxAccountSwitches
	if maxAccountSwitches <= 0 {
		maxAccountSwitches = 3
	}
	failedAccountIDs := make(map[int64]struct{})
	switchCount := 0
	var lastUpstreamErr error

	for {
		var account *service.Account
		if candidateMode {
			account, err = selectCodexDiscoveryAccount(candidateAccounts, failedAccountIDs)
		} else {
			account, err = h.gatewayService.SelectAccountForModelWithExclusions(c.Request.Context(), apiKey.GroupID, "", "", failedAccountIDs)
		}
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
		if err := h.gatewayService.CompleteAPIKeyCodexModelsManifestForClient(manifest, account); err != nil {
			h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to complete Codex models manifest")
			return
		}
		if err := service.ApplyPinnedCodexModelsMapping(manifest, account, apiKey.Group); err != nil {
			h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to apply model mappings")
			return
		}
		if err := h.gatewayService.MergeGroupConfiguredCodexModels(c.Request.Context(), apiKey.Group, manifest, ifNoneMatch); err != nil {
			h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to build Codex models manifest")
			return
		}
		if c.Request.Context().Err() != nil {
			return
		}

		h.writeProjectedCodexModelsResponse(c, manifest, allowedModelIDs, candidateMode)
		return
	}
}

func (h *OpenAIGatewayHandler) writeProjectedCodexModelsResponse(c *gin.Context, manifest *service.OpenAIModelsResponse, allowed map[string]struct{}, candidateMode bool) {
	response := *manifest
	if allowed != nil {
		var err error
		if candidateMode {
			response.Body, err = projectCandidateCodexManifest(response.Body, allowed)
		} else {
			response.Body, err = filterCodexModelsManifest(response.Body, allowed)
		}
		if err != nil {
			h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Codex models manifest could not be filtered")
			return
		}
		response.ETag = service.CodexModelsManifestETag(response.Body)
	}
	response.NotModified = service.CodexModelsManifestETagMatches(c.GetHeader("If-None-Match"), response.ETag)
	writeOpenAIModelsResponse(c, &response)
}

type candidateCodexDiscoverySource interface {
	CandidateSchedulingEnabled() bool
	DiscoverCandidates(context.Context, *service.APIKey, service.UniversalProtocol) ([]service.UniversalCapability, []service.Account, error)
}

func selectCodexDiscoveryAccount(accounts []service.Account, excluded map[int64]struct{}) (*service.Account, error) {
	for i := range accounts {
		if _, failed := excluded[accounts[i].ID]; failed || !accounts[i].IsSchedulable() {
			continue
		}
		return &accounts[i], nil
	}
	return nil, service.ErrNoAvailableAccounts
}

// Preserve upstream manifest metadata. Accounts may expose disjoint models, so
// supported peers absent from this manifest use the same slug-only schema as
// the existing OpenAI model-list adapter.
func projectCandidateCodexManifest(body []byte, allowed map[string]struct{}) ([]byte, error) {
	filtered, err := filterCodexModelsManifest(body, allowed)
	if err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(filtered, &envelope); err != nil {
		return nil, err
	}
	var models []json.RawMessage
	if err := json.Unmarshal(envelope["models"], &models); err != nil {
		return nil, err
	}
	missing := make(map[string]struct{}, len(allowed))
	for id := range allowed {
		missing[id] = struct{}{}
	}
	for _, model := range models {
		var identity struct {
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(model, &identity); err != nil {
			return nil, err
		}
		delete(missing, identity.Slug)
	}
	ids := make([]string, 0, len(missing))
	for id := range missing {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		model, err := json.Marshal(struct {
			Slug string `json:"slug"`
		}{Slug: id})
		if err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	envelope["models"], err = json.Marshal(models)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
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

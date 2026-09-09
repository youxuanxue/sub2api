package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *GatewayHandler) tkServeModels(c *gin.Context) {
	if h.tryServeOpenRouterProviderModels(c) {
		return
	}

	apiKey, _ := middleware2.GetAPIKeyFromContext(c)

	var groupID *int64
	var platform string

	if apiKey != nil && apiKey.Group != nil {
		groupID = &apiKey.Group.ID
		platform = apiKey.Group.Platform
	}
	if forcedPlatform, ok := middleware2.GetForcePlatformFromContext(c); ok && strings.TrimSpace(forcedPlatform) != "" {
		platform = forcedPlatform
	}

	if h.tryServeUniversalModels(c, apiKey, groupID) {
		return
	}
	if apiKey != nil && !apiKey.IsUniversal() && apiKey.Group != nil &&
		platform == service.PlatformOpenAI && apiKey.Group.Platform == service.PlatformOpenAI &&
		apiKey.Group.CodexModelsManifestConfig.Enabled {
		h.pinnedOpenAIModels(c, apiKey.Group)
		return
	}

	// Get available models from account configurations, filtered to the
	// selected group platform so cross-platform model_mapping entries on
	// sibling accounts in the same group don't leak through.
	if platform == service.PlatformComposite {
		availableModels := h.compositeAvailableModels(c.Request.Context(), groupID)
		if apiKey != nil && apiKey.Group != nil && apiKey.Group.ModelAllowlistEnabled() {
			fallbackModels := defaultModelIDsForPlatform(service.PlatformComposite)
			availableModels = modelListingSource(platform, availableModels, fallbackModels)
			if apiKey.Group.CustomModelsListEnabled() {
				availableModels = filterModelsByCustomList(availableModels, fallbackModels, apiKey.Group.ModelsListConfig.Models)
			}
			writeAllowlistedModelsList(c, platform, apiKey.Group.ModelAllowlist.FilterForListing(availableModels))
			return
		}
		if apiKey != nil && apiKey.Group != nil && apiKey.Group.CustomModelsListEnabled() {
			availableModels = filterModelsByCustomList(availableModels, defaultModelIDsForPlatform(service.PlatformComposite), apiKey.Group.ModelsListConfig.Models)
			writeModelsList(c, service.PlatformComposite, availableModels)
			return
		}
		if len(availableModels) > 0 {
			writeModelsList(c, service.PlatformComposite, availableModels)
			return
		}
		writeModelsList(c, service.PlatformComposite, defaultModelIDsForPlatform(service.PlatformComposite))
		return
	}

	// Get available models from account configurations for the selected group platform.
	availableModels := h.gatewayService.GetAvailableModels(c.Request.Context(), groupID, platform)
	// TK: CatalogPolicy projection — priced and not structurally-gone.
	// Transient unreachable stays visible. Nil-safe fail-open.
	availableModels = h.tkFilterModelIDs(c.Request.Context(), platform, availableModels)
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.ModelAllowlistEnabled() {
		fallbackModels := h.servableIDs(c.Request.Context(), platform)
		availableModels = modelListingSource(platform, availableModels, fallbackModels)
		if apiKey.Group.CustomModelsListEnabled() {
			availableModels = filterModelsByCustomList(availableModels, fallbackModels, apiKey.Group.ModelsListConfig.Models)
		}
		writeAllowlistedModelsList(c, platform, apiKey.Group.ModelAllowlist.FilterForListing(availableModels))
		return
	}
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.CustomModelsListEnabled() {
		fallbackModels := h.servableIDs(c.Request.Context(), platform)
		availableModels = filterModelsByCustomList(modelListingSource(platform, availableModels, fallbackModels), fallbackModels, apiKey.Group.ModelsListConfig.Models)
		writeModelsList(c, platform, availableModels)
		return
	}

	if len(availableModels) > 0 {
		writeModelsList(c, platform, availableModels)
		return
	}

	// Fallback to default models.
	//
	// Group platforms participating in the OpenAI-compat pool (today: openai,
	// newapi — see service.OpenAICompatPlatforms) speak the OpenAI HTTP
	// protocol and therefore expect openai.DefaultModels. Without this
	// branch, newapi groups whose accounts have empty model_mapping would
	// silently get Claude default models via the catch-all below — wrong
	// shape for OpenAI-compat clients.
	if service.IsOpenAICompatPlatform(platform) {
		c.JSON(http.StatusOK, gin.H{
			"object": "list",
			"data":   h.tkOpenAIDefaultModelIDs(c.Request.Context(), platform),
		})
		return
	}

	if platform == service.PlatformGemini {
		// TK: same CatalogPolicy projection as /pricing and Your-Menu fallback —
		// drops advertised_dead (e.g. gemini-2.0-flash) instead of returning the
		// raw geminicli.DefaultModels.
		c.JSON(http.StatusOK, gin.H{
			"object": "list",
			"data":   h.tkGeminiDefaultModelsList(c.Request.Context()),
		})
		return
	}
	if platform == service.PlatformGrok {
		writeGrokModelsList(c, xai.DefaultModelIDs())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   h.tkClaudeDefaultModelIDs(c.Request.Context(), platform),
	})
}

func (h *GatewayHandler) compositeAvailableModels(ctx context.Context, groupID *int64) []string {
	if h == nil || h.gatewayService == nil {
		return nil
	}
	seen := make(map[string]struct{})
	models := make([]string, 0)
	schedulablePlatforms := h.gatewayService.GetSchedulablePlatforms(ctx, groupID)
	for _, platform := range []string{service.PlatformAnthropic, service.PlatformGemini, service.PlatformOpenAI, service.PlatformAntigravity, service.PlatformGrok, service.PlatformKimi, service.PlatformZhipu, service.PlatformDeepseek} {
		platformModels := h.gatewayService.GetAvailableModels(ctx, groupID, platform)
		if len(platformModels) == 0 {
			// CN 供应商没有静态默认模型列表（defaultModelIDsForPlatform 的
			// default 分支是 Claude 列表），composite 下只暴露账号映射键。
			if _, ok := schedulablePlatforms[platform]; ok && !service.IsCNProvider(platform) {
				platformModels = defaultModelIDsForPlatform(platform)
			}
		}
		for _, model := range platformModels {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			models = append(models, model)
		}
	}
	return models
}

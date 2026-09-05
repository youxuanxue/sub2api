package handler

import (
	"net/http"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// OpenRouterProviderModels serves GET /openrouter/v1/models for OpenRouter provider onboarding.
// Auth uses the normal API key middleware; access is limited to keys allowed by
// tk_openrouter_provider_config (OR service user/key + dedicated groups).
func (h *GatewayHandler) OpenRouterProviderModels(c *gin.Context) {
	if h.serveOpenRouterProviderModels(c) {
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "openrouter provider catalog unavailable", "code": 503}})
}

func (h *GatewayHandler) serveOpenRouterProviderModels(c *gin.Context) bool {
	if h == nil || h.gatewayService == nil || h.settingService == nil {
		return false
	}

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "missing api key", "code": 401}})
		return true
	}

	cfg, err := h.settingService.GetOpenRouterProviderConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "load openrouter provider config", "code": 500}})
		return true
	}
	if !cfg.CanAccessCatalog(apiKey.ID, apiKey.UserID, apiKey.Name) {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"message": "api key not authorized for openrouter provider catalog", "code": 403}})
		return true
	}

	catalog, err := h.gatewayService.BuildOpenRouterProviderCatalog(c.Request.Context(), cfg)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error(), "code": 400}})
		return true
	}
	c.JSON(http.StatusOK, catalog)
	return true
}

func (h *GatewayHandler) tryServeOpenRouterProviderModels(c *gin.Context) bool {
	if h == nil || h.settingService == nil {
		return false
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		return false
	}
	cfg, err := h.settingService.GetOpenRouterProviderConfig(c.Request.Context())
	if err != nil || !cfg.CanAccessCatalog(apiKey.ID, apiKey.UserID, apiKey.Name) {
		return false
	}
	return h.serveOpenRouterProviderModels(c)
}

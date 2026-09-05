package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

func geminiV1BetaGroupPlatformAllowed(apiKey *service.APIKey) bool {
	if apiKey == nil || apiKey.Group == nil {
		return false
	}
	switch apiKey.Group.Platform {
	case service.PlatformGemini, service.PlatformAntigravity:
		return true
	case service.PlatformNewAPI:
		// Direct newapi keys remain outside the native Gemini surface. Universal
		// routing has already proved exact Vertex account/model serviceability
		// before replacing the key's backing group.
		return apiKey.IsUniversal()
	default:
		return false
	}
}

// tkGeminiV1BetaTryForceAntigravityListModels serves the static Antigravity
// model list when the request is on a force-platform Antigravity route.
func (h *GatewayHandler) tkGeminiV1BetaTryForceAntigravityListModels(c *gin.Context, forcePlatform string) bool {
	if forcePlatform != service.PlatformAntigravity {
		return false
	}
	c.JSON(http.StatusOK, antigravity.FallbackGeminiModelsList())
	return true
}

// tkGeminiV1BetaListModelsCatalogFallback writes the CatalogPolicy-projected
// Gemini models list used when Gemini accounts are absent or upstream falls back.
func (h *GatewayHandler) tkGeminiV1BetaListModelsCatalogFallback(c *gin.Context) {
	c.JSON(http.StatusOK, h.tkGeminiFallbackModelsList(c.Request.Context()))
}

// tkGeminiV1BetaTryForceAntigravityGetModel serves Antigravity static model
// metadata for force-platform Antigravity routes.
func (h *GatewayHandler) tkGeminiV1BetaTryForceAntigravityGetModel(c *gin.Context, forcePlatform, modelName string) bool {
	if forcePlatform != service.PlatformAntigravity {
		return false
	}
	c.JSON(http.StatusOK, antigravity.FallbackGeminiModel(modelName))
	return true
}

// tkGeminiV1BetaGetModelAntigravityFallback writes static Gemini model metadata
// when only Antigravity accounts are available for the group.
func (h *GatewayHandler) tkGeminiV1BetaGetModelAntigravityFallback(c *gin.Context, modelName string) {
	c.JSON(http.StatusOK, gemini.FallbackModel(modelName))
}

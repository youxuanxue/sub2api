package handler

import (
	"encoding/json"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// geminiV1BetaGroupPlatformAllowedForPlatform gates the native Gemini surface by
// billing group. effectivePlatform is the platform a composite route already
// resolved for this request ("" when the caller has not resolved one).
//
// A composite group has no single platform of its own: compositeGeminiTarget
// resolves the concrete target per request before the handler runs. Gating it on
// Group.Platform alone rejects every composite /v1beta request with "API key
// group cannot serve native Gemini requests", so the model-scoped check below
// (servesCompositeAntigravityGemini + isAntigravityGeminiModel) never runs.
func geminiV1BetaGroupPlatformAllowedForPlatform(apiKey *service.APIKey, effectivePlatform string) bool {
	if apiKey == nil || apiKey.Group == nil {
		return false
	}
	switch apiKey.Group.Platform {
	case service.PlatformGemini, service.PlatformAntigravity:
		return true
	case service.PlatformComposite:
		// Only a Gemini-family target reaches the native surface; the requested
		// model is still screened per request by the callers.
		return effectivePlatform == service.PlatformGemini || effectivePlatform == service.PlatformAntigravity
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
func (h *GatewayHandler) tkGeminiV1BetaTryForceAntigravityListModels(c *gin.Context, forcePlatform string, models []gemini.Model) bool {
	if forcePlatform != service.PlatformAntigravity {
		return false
	}
	writeGroupGeminiModels(c, gemini.ModelsListResponse{Models: models})
	return true
}

// tkGeminiV1BetaListModelsCatalogFallback writes the CatalogPolicy-projected
// Gemini models list used when Gemini accounts are absent or upstream falls back.
func (h *GatewayHandler) tkGeminiV1BetaListModelsCatalogFallback(c *gin.Context) {
	writeGroupGeminiModels(c, h.tkGeminiFallbackModelsList(c.Request.Context()))
}

func writeGroupGeminiModels(c *gin.Context, models any) {
	key, _ := middleware.GetAPIKeyFromContext(c)
	if key != nil && !key.IsUniversal() && key.Group != nil && key.Group.ModelAllowlistEnabled() {
		body, err := json.Marshal(models)
		if err != nil {
			googleError(c, http.StatusInternalServerError, "Failed to build model list")
			return
		}
		filtered, _, ok := filterUpstreamGeminiModelsBody(body, key.Group.ModelAllowlist)
		if !ok {
			googleError(c, http.StatusInternalServerError, "Failed to filter model list")
			return
		}
		c.Data(http.StatusOK, "application/json", filtered)
		return
	}
	c.JSON(http.StatusOK, models)
}

// tkGeminiV1BetaTryForceAntigravityGetModel serves Antigravity static model
// metadata for force-platform Antigravity routes, and for a composite group
// whose route already resolved to Antigravity.
//
// Antigravity has no AI Studio models endpoint, so an Antigravity-resolved
// request must answer from static metadata. Without the composite branch the
// handler falls through to SelectAccountForModelsEndpoints, which looks for a
// native Gemini account the composite route never promised.
func (h *GatewayHandler) tkGeminiV1BetaTryForceAntigravityGetModel(c *gin.Context, apiKey *service.APIKey, forcePlatform, effectivePlatform, modelName string) bool {
	switch {
	case forcePlatform == service.PlatformAntigravity:
	case servesCompositeAntigravityGemini(apiKey, effectivePlatform):
		// Composite only. A plain Antigravity group keeps falling through to the
		// account-selection path so its CatalogPolicy fallback still applies.
	default:
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

// Candidate ingress already bounds authorization; the actual account and Plan
// decide protocol compatibility. A backing billing group is not an execution gate.
func geminiV1BetaRequestPlatformAllowed(c *gin.Context, key *service.APIKey) bool {
	return key != nil && ((c != nil && c.Request != nil && service.CandidateRequestFromContext(c.Request.Context()) != nil) ||
		geminiV1BetaGroupPlatformAllowedForPlatform(key, effectiveAPIKeyPlatform(c, key)))
}

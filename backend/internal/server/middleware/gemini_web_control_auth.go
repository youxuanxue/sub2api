package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type geminiWebKeyLookup interface {
	GetByKey(ctx context.Context, key string) (*service.APIKey, error)
}

const GeminiWebCallerAPIKeyCtxKey = "gemini_web_caller_apikey"

func NewGeminiWebControlAuthMiddleware(lookup geminiWebKeyLookup) gin.HandlerFunc {
	return func(c *gin.Context) {
		if lookup == nil {
			AbortWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Gemini Web control auth unavailable")
			return
		}

		keyString := extractGeminiWebKey(c)
		if keyString == "" {
			AbortWithError(c, http.StatusUnauthorized, "API_KEY_REQUIRED", "API key is required (x-api-key header or Bearer token)")
			return
		}

		apiKey, err := lookup.GetByKey(c.Request.Context(), keyString)
		if err != nil {
			if errors.Is(err, service.ErrAPIKeyNotFound) {
				AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
				return
			}
			if IsClientClosedRequestError(c, err) {
				AbortClientClosedRequest(c, err)
				return
			}
			AbortWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to validate API key")
			return
		}
		// Control calls accept exhausted/expired billing quotas but reject disabled keys.
		if apiKey == nil || (!apiKey.IsActive() &&
			apiKey.Status != service.StatusAPIKeyExpired &&
			apiKey.Status != service.StatusAPIKeyQuotaExhausted) {
			AbortWithError(c, http.StatusUnauthorized, "API_KEY_DISABLED", "API key is disabled")
			return
		}

		// Expose the authenticated caller key for group-scoped reads.
		c.Set(GeminiWebCallerAPIKeyCtxKey, apiKey)
		c.Next()
	}
}

func extractGeminiWebKey(c *gin.Context) string {
	if authHeader := c.GetHeader("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if k := strings.TrimSpace(parts[1]); k != "" {
				return k
			}
		}
	}
	return strings.TrimSpace(c.GetHeader("x-api-key"))
}

package middleware

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type geminiWebAdminKeyLookup interface {
	GetByKey(ctx context.Context, key string) (*service.APIKey, error)
}

type geminiWebAdminUserLookup interface {
	GetByID(ctx context.Context, id int64) (*service.User, error)
}

func NewGeminiWebAdminOwnerMiddleware(keys geminiWebAdminKeyLookup, users geminiWebAdminUserLookup) gin.HandlerFunc {
	return func(c *gin.Context) {
		if keys == nil || users == nil {
			AbortWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Gemini Web admin auth unavailable")
			return
		}

		key := extractGeminiWebKey(c)
		if key == "" {
			AbortWithError(c, http.StatusUnauthorized, "API_KEY_REQUIRED", "API key is required (x-api-key header or Bearer token)")
			return
		}

		apiKey, err := keys.GetByKey(c.Request.Context(), key)
		if err != nil || apiKey == nil {
			AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
			return
		}

		user, err := users.GetByID(c.Request.Context(), apiKey.UserID)
		if err != nil || user == nil {
			AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "API key owner not found")
			return
		}

		// The elevation gate: only an admin-owned, active key may reach the control route.
		if !user.IsAdmin() || !user.IsActive() {
			AbortWithError(c, http.StatusForbidden, "ADMIN_REQUIRED", "API key owner must be an active admin")
			return
		}

		c.Next()
	}
}

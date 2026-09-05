package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MaybeRewriteOpenRouterProviderChatBody rewrites tokenkey/<model> to the internal
// scheduling id for OpenRouter inference keys before universal routing peeks the
// model and before handlers validate servable models. Applies to any JSON POST
// body carrying a model field (chat, images, videos).
func MaybeRewriteOpenRouterProviderChatBody(c *gin.Context, apiKey *service.APIKey, settingService *service.SettingService) {
	if c == nil || c.Request == nil || apiKey == nil || settingService == nil {
		return
	}
	if c.Request.Body == nil {
		return
	}
	method := c.Request.Method
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
		return
	}

	raw, ok := readAndRestoreUniversalBody(c)
	if !ok || len(raw) == 0 {
		return
	}
	peekBytes := universalPeekBytes(c, raw)
	if len(peekBytes) == 0 {
		return
	}

	userID := apiKey.UserID
	if apiKey.User != nil && apiKey.User.ID > 0 {
		userID = apiKey.User.ID
	}
	normalized, _, changed, err := settingService.NormalizeOpenRouterProviderChatBody(
		c.Request.Context(), apiKey.ID, userID, apiKey.Name, peekBytes,
	)
	if err != nil || !changed {
		return
	}

	// Preserve any bytes outside the decoded JSON window (should be empty for plain JSON).
	prefixLen := len(raw) - len(peekBytes)
	if prefixLen < 0 {
		prefixLen = 0
	}
	var rebuilt []byte
	if prefixLen > 0 {
		rebuilt = append(rebuilt, raw[:prefixLen]...)
	}
	rebuilt = append(rebuilt, normalized...)
	c.Request.Body = io.NopCloser(bytes.NewReader(rebuilt))
	c.Request.ContentLength = int64(len(rebuilt))
}

// MaybeRejectOpenRouterProviderMonitorInference blocks OR monitor-only keys from
// seller inference paths (chat + /openrouter/v1/* except catalog GET) before
// universal routing runs.
func MaybeRejectOpenRouterProviderMonitorInference(c *gin.Context, apiKey *service.APIKey, settingService *service.SettingService) bool {
	if c == nil || c.Request == nil || apiKey == nil || settingService == nil {
		return false
	}
	if !openRouterProviderMonitorInferencePath(c.Request.Method, c.Request.URL.Path) {
		return false
	}
	cfg, err := settingService.GetOpenRouterProviderConfig(c.Request.Context())
	if err != nil || !cfg.Enabled {
		return false
	}
	userID := apiKey.UserID
	if apiKey.User != nil && apiKey.User.ID > 0 {
		userID = apiKey.User.ID
	}
	if !cfg.AllowsMonitorAPIKey(apiKey.ID, userID, apiKey.Name) {
		return false
	}
	if cfg.AllowsInferenceAPIKey(apiKey.ID, userID, apiKey.Name) {
		return false
	}
	c.JSON(http.StatusForbidden, gin.H{
		"error": gin.H{
			"message": "api key not authorized for openrouter provider inference",
			"code":    403,
		},
	})
	c.Abort()
	return true
}

func openRouterProviderMonitorInferencePath(method, path string) bool {
	path = strings.TrimSpace(path)
	method = strings.ToUpper(strings.TrimSpace(method))
	if path == "/openrouter/v1/models" && method == http.MethodGet {
		return false
	}
	if strings.HasPrefix(path, "/openrouter/v1/") {
		return true
	}
	switch path {
	case "/v1/chat/completions", "/chat/completions":
		return method == http.MethodPost
	default:
		return false
	}
}

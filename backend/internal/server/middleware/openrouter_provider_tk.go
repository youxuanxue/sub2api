package middleware

import (
	"bytes"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MaybeRewriteOpenRouterProviderChatBody rewrites tokenkey/<model> to the internal
// scheduling id for OpenRouter seller keys before universal routing peeks the
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
		c.Request.Context(), apiKey.ID, userID, peekBytes,
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

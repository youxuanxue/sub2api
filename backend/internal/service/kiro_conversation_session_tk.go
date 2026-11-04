package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// kiroResolvedConversationSessionID returns the client session already resolved
// on this Kiro forward. It reuses DeriveStickyKey's client order: explicit
// sticky headers, metadata.user_id, x-session-affinity, then prompt_cache_key.
// It does not hash message content and does not mint an id. An empty result
// leaves a synthetic Kiro conversation anchor on a fresh UUID.
func kiroResolvedConversationSessionID(c *gin.Context, parsed *ParsedRequest, body []byte) string {
	var headers http.Header
	if c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	// Passthrough reads client keys only. The group's sticky mode is not a
	// reason to invent a new conversation for a client that already named one.
	key := DeriveStickyKey(StickyInjectionRequest{
		Strategy: StickyStrategy{GlobalEnabled: true, Mode: StickyModePassthrough},
		Headers:  headers,
	}, body)
	if v := strings.TrimSpace(key.Value); v != "" {
		return v
	}
	if parsed != nil {
		if v := strings.TrimSpace(parsed.ExplicitStickyKey.Value); v != "" {
			return v
		}
	}
	return ""
}

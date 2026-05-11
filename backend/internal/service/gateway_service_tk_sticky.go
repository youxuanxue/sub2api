package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ClaudeCodeParsedRequestGinKey stores *ParsedRequest for ingress Claude Code /
// Messages paths. Downstream hops (prod → sibling gateway) may serialize a body
// that no longer exposes metadata.user_id to gjson; this snapshot preserves the
// client session id for X-Claude-Code-Session-Id fallback.
const ClaudeCodeParsedRequestGinKey = "claude_code_parsed_request"

func tkSyncClaudeCodeSessionHeaderFromBody(headers http.Header, body []byte) {
	if uid := gjson.GetBytes(body, "metadata.user_id").String(); uid != "" {
		if parsed := ParseMetadataUserID(uid); parsed != nil && parsed.SessionID != "" {
			setHeaderRaw(headers, "X-Claude-Code-Session-Id", parsed.SessionID)
		}
	}
}

// tkEnsureClaudeCodeSessionHeader sets X-Claude-Code-Session-Id from the outbound
// body first, then from ingress ParsedRequest (Gin) when the body lacks a
// parsable Claude Code identity. Keeps sibling gateways on a stable sticky key.
func tkEnsureClaudeCodeSessionHeader(headers http.Header, body []byte, c *gin.Context) {
	tkSyncClaudeCodeSessionHeaderFromBody(headers, body)
	if strings.TrimSpace(getHeaderRaw(headers, "X-Claude-Code-Session-Id")) != "" || headers == nil {
		return
	}
	if c == nil {
		return
	}
	v, ok := c.Get(ClaudeCodeParsedRequestGinKey)
	if !ok {
		return
	}
	pr, ok := v.(*ParsedRequest)
	if !ok || pr == nil || strings.TrimSpace(pr.MetadataUserID) == "" {
		return
	}
	if parsed := ParseMetadataUserID(strings.TrimSpace(pr.MetadataUserID)); parsed != nil && parsed.SessionID != "" {
		setHeaderRaw(headers, "X-Claude-Code-Session-Id", parsed.SessionID)
	}
}

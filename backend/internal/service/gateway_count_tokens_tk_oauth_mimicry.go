package service

import (
	"bytes"
	"context"

	"github.com/tidwall/gjson"

	"github.com/gin-gonic/gin"
)

// tkApplyCountTokensOAuthMimicry applies Claude Code OAuth mimicry rewrites on
// count_tokens when the account is OAuth and the client is not Claude Code.
// Body mutations go through replaceBody so the ParsedRequest buffer stays in
// sync (same eager semantics as the pre-companion inline path).
func (s *GatewayService) tkApplyCountTokensOAuthMimicry(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	reqModel string,
	metadataUserID string,
	replaceBody func(next []byte) error,
	getBody func() []byte,
) (shouldMimicClaudeCode bool, nextModel string, err error) {
	nextModel = reqModel
	isClaudeCodeCT := IsClaudeCodeClient(ctx) || isClaudeCodeClient(c.GetHeader("User-Agent"), metadataUserID)
	shouldMimicClaudeCode = account.IsOAuth() && !isClaudeCodeCT
	if !shouldMimicClaudeCode {
		return false, nextModel, nil
	}

	body := getBody()
	normalizeOpts := claudeOAuthNormalizeOptions{stripSystemCacheControl: true}
	hadContextManagement := gjson.GetBytes(body, "context_management").Exists()
	var normalizedBody []byte
	normalizedBody, nextModel = normalizeClaudeOAuthRequestBody(body, nextModel, normalizeOpts)
	if !hadContextManagement && gjson.GetBytes(normalizedBody, "context_management").Exists() {
		if stripped, ok := deleteJSONPathBytes(normalizedBody, "context_management"); ok {
			normalizedBody = stripped
		}
	}
	if err := replaceBody(normalizedBody); err != nil {
		return shouldMimicClaudeCode, nextModel, err
	}

	body = getBody()
	if err := replaceBody(s.rewriteMessageCacheControlIfEnabled(ctx, body)); err != nil {
		return shouldMimicClaudeCode, nextModel, err
	}
	body = getBody()
	if rw := buildToolNameRewriteFromBody(body); rw != nil {
		if err := replaceBody(applyToolNameRewriteToBody(body, rw)); err != nil {
			return shouldMimicClaudeCode, nextModel, err
		}
	} else {
		if err := replaceBody(applyToolsLastCacheBreakpoint(body)); err != nil {
			return shouldMimicClaudeCode, nextModel, err
		}
	}
	body = getBody()
	if strippedBody, _ := StripCountTokensUnsupportedFields(body); !bytes.Equal(strippedBody, body) {
		if err := replaceBody(strippedBody); err != nil {
			return shouldMimicClaudeCode, nextModel, err
		}
	}
	return shouldMimicClaudeCode, nextModel, nil
}

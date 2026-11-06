package service

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// tkApplyAnthropicAPIKeyCompatSticky gives Chat Completions / Responses
// requests converted to Anthropic Messages the same sticky session that
// /v1/messages gets from its pre-send guard. Session-requiring upstreams
// reject a converted body without metadata.user_id.
//
// Only Anthropic-platform API Key accounts qualify: OAuth goes through Claude
// Code mimicry, and Bedrock / Kiro are separate account types or platforms.
// isClaudeCode is always false because these protocols never come from the
// Claude Code client.
//
// The OpenAI request structs carry no metadata, so conversion drops a
// client-supplied user_id. Restore it first; the injector never rewrites an
// existing value.
func tkApplyAnthropicAPIKeyCompatSticky(
	ctx context.Context,
	c *gin.Context,
	settingService stickyGlobalEnabledProvider,
	account *Account,
	clientBody []byte,
	anthropicBody []byte,
	mappedModel string,
) ([]byte, error) {
	if account == nil || account.Platform != PlatformAnthropic || account.Type != AccountTypeAPIKey {
		return anthropicBody, nil
	}
	if clientUID := gjson.GetBytes(clientBody, "metadata.user_id"); clientUID.Type == gjson.String {
		uid := strings.TrimSpace(clientUID.String())
		if uid != "" && strings.TrimSpace(gjson.GetBytes(anthropicBody, "metadata.user_id").String()) == "" {
			patched, err := sjson.SetBytes(anthropicBody, "metadata.user_id", uid)
			if err != nil {
				return nil, err
			}
			anthropicBody = patched
		}
	}
	stickyBody, _, err := applyStickyToAnthropicMessagesBody(ctx, c, settingService, account, anthropicBody, mappedModel, false)
	if err != nil {
		return nil, err
	}
	return stickyBody, nil
}

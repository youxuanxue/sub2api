package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// tkShouldApplyMessagesDispatchBodyMapping reports whether group-level
// messages-dispatch claude→gpt body rewrite should run for this account on
// /v1/responses forward attempts.
//
// Tokensea-style OpenAI API-key relays expose native /v1/messages and CC-only
// upstreams: rewriting claude family names to configured gpt dispatch models
// breaks account selection (scheduler sees gpt IDs absent from relay mapping)
// and forward (upstream expects Claude wire IDs). OAuth Codex accounts still
// need the rewrite so ChatGPT backends do not reject raw claude names.
func tkShouldApplyMessagesDispatchBodyMapping(account *service.Account) bool {
	if account == nil {
		return true
	}
	if account.Type != service.AccountTypeAPIKey {
		return true
	}
	if openai_compat.ShouldUseNativeAnthropicMessagesAPI(account.Extra) {
		return false
	}
	if !openai_compat.ShouldUseResponsesAPI(account.Extra) {
		return false
	}
	return true
}

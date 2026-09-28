package service

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TokenKey: tokensea (agent.tokensea.ai) pre-send body strips.
//
// Account-gated only — not model-gated. Official Anthropic / Cursor keep the
// original fields; tokensea's stricter schema 400s them as
// "Extra inputs are not permitted" on every Claude model seen in prod.
// Callers go through tkPrepareAnthropicMessagesWireBody so native Messages,
// Forward, and API-key passthrough share one owner.

// isTokenseaRelayUpstream reports whether this account's wire base_url is
// agent.tokensea.ai. Covers openai / anthropic typed relays plus newapi
// Anthropic-channel accounts (prod account 136) that share the same host.
func isTokenseaRelayUpstream(account *Account) bool {
	if account == nil {
		return false
	}
	if account.IsOpenAITokenseaRelay() || account.IsAnthropicTokenseaRelay() {
		return true
	}
	return isTokenseaRelayBaseURL(account.GetCredential("base_url"))
}

// tkStripTokenseaContextManagement drops body.context_management before
// forwarding any model to agent.tokensea.ai.
//
// Tokensea's Anthropic schema rejects context_management with HTTP 400
// "Extra inputs are not permitted" across models — not only Fable:
//   - 2026-09-20 user16: account 136 × claude-fable-5 (ops_error 4222978)
//   - 2026-09-20: account 136 × claude-opus-4-8 (3 finals)
//   - 2026-09-26 user1: account 136 × claude-opus-5 (ops_error 5275273+)
//
// The earlier fable-only gate (tkStripTokenseaFableContextManagement) let
// Opus variants keep recurring. Cursor accepts the same client payload; strip
// is account-gated only so non-tokensea upstreams keep context_management.
func tkStripTokenseaContextManagement(account *Account, body []byte) []byte {
	if len(body) == 0 || !isTokenseaRelayUpstream(account) {
		return body
	}
	if !gjson.GetBytes(body, "context_management").Exists() {
		return body
	}
	stripped, err := sjson.DeleteBytes(body, "context_management")
	if err != nil {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	logger.LegacyPrintf("service.gateway",
		"[Forward] stripped context_management for tokensea before upstream forward (tokensea returns 400 Extra inputs are not permitted): account=%d model=%s original_bytes=%d stripped_bytes=%d",
		account.ID, model, len(body), len(stripped))
	return stripped
}

// tkStripTokenseaCacheControlScope drops cache_control.scope before forwarding
// to agent.tokensea.ai. Official Anthropic accepts scope with beta
// prompt-caching-scope-2026-01-05; tokensea's schema rejects it with HTTP 400
// "system.N.cache_control.***.scope: Extra inputs are not permitted".
//
// Evidence (prod 2026-09-28 user 16): account 136 × claude-opus-5 ×
// claude-cli/2.1.92, ops_error 5276634/5276647/5276648. Same payload succeeds
// on kiro-us* (translator drops cache_control) and official Anthropic OAuth.
// Strip is account-gated so non-tokensea upstreams keep scope. Walks the same
// cache_control locations as collectCacheControlPaths (system / messages /
// tools / top-level); type and ttl stay.
func tkStripTokenseaCacheControlScope(account *Account, body []byte) []byte {
	if len(body) == 0 || !isTokenseaRelayUpstream(account) {
		return body
	}
	_, topLevel, messagePaths, toolPaths, systemPaths := collectCacheControlPaths(body)
	paths := make([]string, 0, len(topLevel)+len(messagePaths)+len(toolPaths)+len(systemPaths))
	paths = append(paths, topLevel...)
	paths = append(paths, systemPaths...)
	paths = append(paths, messagePaths...)
	paths = append(paths, toolPaths...)
	original := len(body)
	changed := false
	for _, path := range paths {
		if !gjson.GetBytes(body, path+".scope").Exists() {
			continue
		}
		next, err := sjson.DeleteBytes(body, path+".scope")
		if err != nil {
			continue
		}
		body = next
		changed = true
	}
	if !changed {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	logger.LegacyPrintf("service.gateway",
		"[Forward] stripped cache_control.scope for tokensea before upstream forward (tokensea returns 400 Extra inputs are not permitted): account=%d model=%s original_bytes=%d stripped_bytes=%d",
		account.ID, model, original, len(body))
	return body
}

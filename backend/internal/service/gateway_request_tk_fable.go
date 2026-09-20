package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TokenKey: Fable-tier model predicates for thinking-shape rectification.
//
// Fable 5 (`claude-fable-5`) is the new tier above Opus. Captured real Claude
// Code 2.1.170 traffic (egress 3.148.79.145, 2026-06-09; see
// docs/spec-delta/cc-fable-5.md) shows Fable shares Opus 4.7+'s request surface:
// manual thinking (`thinking:{type:"enabled",budget_tokens:N}`) is rejected with
// the same adaptive-required 400, and only `{type:"adaptive"}` is accepted.
//
// The existing budget/adaptive rectifiers hard-gate on isOpus47OrNewer, whose
// name (and the shared claudeVersionRe regex) only matches opus/sonnet/haiku
// version strings — `claude-fable-5` matches neither, so without this widening
// Fable's adaptive-required 400 would be gated OUT of the reactive repair and
// surfaced raw to the client. requiresAdaptiveOnlyThinking is the broadened gate
// used at those call sites; isOpus47OrNewer stays honest to its name.
//
// Disabled semantics (prod 2026-06-10, user 16): Fable also rejects an explicit
// `thinking:{"type":"disabled"}` with a 400 ("thinking.type.disabled" is not
// supported for this model …), while Opus 4.7+ still ACCEPTS explicit disabled —
// so the proactive strip below gates on isFableModel, NOT on
// requiresAdaptiveOnlyThinking. Omitting the field is the documented equivalent.
// Precedent: the bedrock path already does exactly this (bedrock_request.go:683,
// sanitizeBedrockThinking "disabled" case); see docs/spec-delta/cc-fable-5.md.

// isFableModel reports whether modelID is a Claude Fable-tier model. Matches the
// bare id, dated snapshots, the [1m] context-window alias, and the bedrock
// `anthropic.claude-fable-5` form via a single substring test.
func isFableModel(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "fable")
}

// requiresAdaptiveOnlyThinking reports whether the model rejects manual thinking
// (`thinking:{type:"enabled",budget_tokens:N}` → 400) and accepts only
// `{type:"adaptive"}`. True for Opus 4.7+ and all Fable-tier models. This is the
// platform-neutral gate; the bedrock path mirrors it via isBedrockOpus47OrNewer
// || isFableModel in sanitizeBedrockThinking.
func requiresAdaptiveOnlyThinking(modelID string) bool {
	return isOpus47OrNewer(modelID) || isFableModel(modelID)
}

// tkStripFableDisabledThinking proactively drops an explicit
// `thinking:{"type":"disabled"}` before the outbound send when the body targets
// a Fable-tier model (upstream rejects explicit disabled with a 400; omitting
// the field is equivalent — see the file-header note and bedrock_request.go:683
// for the same rule on the bedrock path). Any other shape — non-fable model,
// adaptive/enabled thinking, or no thinking at all — returns the input bytes
// unchanged (no reformat). Surgical delete via sjson keeps every other byte
// intact. The reactive 400 detector stays anchored on "enabled" only: once this
// proactive strip runs, the disabled shape never reaches upstream.
func tkStripFableDisabledThinking(body []byte) []byte {
	model := gjson.GetBytes(body, "model").String()
	if !isFableModel(model) || gjson.GetBytes(body, "thinking.type").String() != "disabled" {
		return body
	}
	stripped, err := sjson.DeleteBytes(body, "thinking")
	if err != nil {
		return body
	}
	logger.LegacyPrintf("service.gateway",
		"[Forward] stripped explicit thinking.type=disabled for fable model before upstream forward (fable returns 400 on explicit disabled; omitted field is equivalent): model=%s original_bytes=%d stripped_bytes=%d",
		model, len(body), len(stripped))
	return stripped
}

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

// tkStripTokenseaFableContextManagement drops body.context_management before
// forwarding Fable models to agent.tokensea.ai.
//
// Prod 2026-09-20 user 16 (ops_error_logs id 4222978): Claude CLI 2.1.276 →
// Cursor Connect 429 → failover to tokensea/anthropic (account 136) returned
// HTTP 400 "context_management: Extra inputs are not permitted" for
// claude-fable-5. Cursor accepts the same client payload; 7d prod only saw
// this 400 on account 136 × fable. Strip is surgical and account+model gated
// so Sonnet/Opus on tokensea and Fable on Cursor keep context_management.
func tkStripTokenseaFableContextManagement(account *Account, body []byte) []byte {
	if len(body) == 0 || !isTokenseaRelayUpstream(account) {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	if !isFableModel(model) || !gjson.GetBytes(body, "context_management").Exists() {
		return body
	}
	stripped, err := sjson.DeleteBytes(body, "context_management")
	if err != nil {
		return body
	}
	logger.LegacyPrintf("service.gateway",
		"[Forward] stripped context_management for tokensea+fable before upstream forward (tokensea returns 400 Extra inputs are not permitted): account=%d model=%s original_bytes=%d stripped_bytes=%d",
		account.ID, model, len(body), len(stripped))
	return stripped
}

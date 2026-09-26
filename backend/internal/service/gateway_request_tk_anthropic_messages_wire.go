package service

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

// Anthropic Messages wire-body + thinking-contract SSOT (TokenKey).
//
// Two owners only:
//   - tkPrepareAnthropicMessagesWireBody: egress thinking-contract filters every
//     Anthropic-wire path must apply before the first upstream hop.
//   - tkRectifyAnthropicThinkingContract400: body mutation for Anthropic 400s
//     that are recoverable by thinking/tool downgrade (shared by Forward,
//     APIKey passthrough, native messages, count_tokens).
//
// Path-specific filters (UTF-8 sanitize, StripEmptyTextBlocks, web-search
// history, beta-token body sanitize, mimicry) stay at their existing call
// sites. This file owns the thinking-contract subset that was previously
// wired on OAuth Forward + APIKey passthrough only — and missed tokensea
// native messages (prod 2026-09-25 user16 claude-fable-5).

// tkPrepareAnthropicMessagesWireBody applies the shared Anthropic Messages
// thinking-contract egress filters:
//  1. FilterThinkingBlocks (drop invalid/missing signatures on anthropic-strict)
//  2. TkPrefilterToolSearchHistoricalThinking (claude-code #63792 / #10199)
//  3. tkRepairHistoricalAssistantTrailingThinking (final-block-cannot-be-thinking)
//  4. tkStripTokenseaContextManagement (tokensea rejects CM on all models)
//
// Callers that already applied (1)/(4) may call this anyway — all steps are
// idempotent on a clean body.
func tkPrepareAnthropicMessagesWireBody(account *Account, body []byte, mappedModel string) []byte {
	body = FilterThinkingBlocks(body, mappedModel)
	body = TkPrefilterToolSearchHistoricalThinking(body, mappedModel)
	body = tkRepairHistoricalAssistantTrailingThinking(body, mappedModel)
	body = tkStripTokenseaContextManagement(account, body)
	return body
}

// tkApplyAnthropicThinkingContractPrefilters is the thinking-only subset for
// paths that already own tokensea CM stripping later in buildUpstream /
// passthrough HTTP builders.
func tkApplyAnthropicThinkingContractPrefilters(body []byte, mappedModel string) []byte {
	body = FilterThinkingBlocks(body, mappedModel)
	body = TkPrefilterToolSearchHistoricalThinking(body, mappedModel)
	body = tkRepairHistoricalAssistantTrailingThinking(body, mappedModel)
	return body
}

func isAnthropicThinkingCannotBeModifiedError(respBody []byte) bool {
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "cannot be modified") &&
		(strings.Contains(msg, "thinking") || strings.Contains(msg, "redacted_thinking"))
}

func isAnthropicFinalBlockThinkingError(respBody []byte) bool {
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "final block") && strings.Contains(msg, "thinking")
}

// isAnthropicThinkingContractErrorMessage mirrors GatewayService.isThinkingBlockSignatureError
// without the logger side effects — used by the shared rectifier and tests.
func isAnthropicThinkingContractErrorMessage(msg string) bool {
	msg = strings.ToLower(strings.TrimSpace(msg))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "signature") {
		return true
	}
	if strings.Contains(msg, "expected") && (strings.Contains(msg, "thinking") || strings.Contains(msg, "redacted_thinking")) {
		return true
	}
	if strings.Contains(msg, "cannot be modified") && (strings.Contains(msg, "thinking") || strings.Contains(msg, "redacted_thinking")) {
		return true
	}
	if strings.Contains(msg, "final block") && strings.Contains(msg, "thinking") {
		return true
	}
	if strings.Contains(msg, "non-empty content") || strings.Contains(msg, "empty content") ||
		strings.Contains(msg, "content blocks must be non-empty") {
		return true
	}
	if strings.Contains(msg, "thinking block must contain") {
		return true
	}
	return false
}

// tkRectifyAnthropicThinkingContract400 returns a repaired body for Anthropic
// thinking-contract 400s. For "cannot be modified" it strips historical signed
// thinking first (gate-free), then escalates to FilterSignatureSensitiveBlocksForRetry
// when the latest assistant turn is tool-coupled / prefill-preserved. For
// "final block … thinking" it repairs trailing thinking on historical turns.
func tkRectifyAnthropicThinkingContract400(body []byte, mappedModel string, respBody []byte) (newBody []byte, kind string, ok bool) {
	if !ShouldApplyRetryFilters(mappedModel) {
		return body, "", false
	}
	msg := extractUpstreamErrorMessage(respBody)
	if !isAnthropicThinkingContractErrorMessage(msg) {
		return body, "", false
	}
	if isAnthropicFinalBlockThinkingError(respBody) {
		repaired := tkRepairHistoricalAssistantTrailingThinking(body, mappedModel)
		if !bytes.Equal(repaired, body) {
			return repaired, "final_block_thinking_repair", true
		}
		// Trailing repair was a no-op (e.g. prefill-only); fall through to
		// historical strip / signature retry like other thinking-contract 400s.
	}
	if isAnthropicThinkingCannotBeModifiedError(respBody) {
		stripped := tkStripHistoricalAssistantThinking(body)
		if !bytes.Equal(stripped, body) {
			return stripped, "thinking_cannot_modify_strip_historical", true
		}
		out := FilterSignatureSensitiveBlocksForRetry(body, mappedModel)
		return out, "thinking_cannot_modify_signature_sensitive", true
	}
	return FilterThinkingBlocksForRetry(body, mappedModel), "signature_retry_thinking", true
}

// tkMappedModelFromAnthropicBody prefers an explicit mapped model, else body.model.
func tkMappedModelFromAnthropicBody(body []byte, mappedModel string) string {
	if strings.TrimSpace(mappedModel) != "" {
		return mappedModel
	}
	return strings.TrimSpace(gjson.GetBytes(body, "model").String())
}

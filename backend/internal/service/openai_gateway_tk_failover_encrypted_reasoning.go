package service

import (
	"bytes"
	"encoding/json"
)

// encryptedContentMarker is the substring that must be present for a Responses
// request body to carry OpenAI reasoning `encrypted_content`. Used as a cheap
// pre-filter so the common case (no encrypted reasoning, or a Chat Completions
// body that has no `input` array at all) pays zero parse cost.
var encryptedContentMarker = []byte(`"encrypted_content"`)

// TkStripEncryptedReasoningForFailover removes OpenAI Responses
// `encrypted_content` from reasoning `input` items in a JSON request body,
// returning the rewritten body (or the original bytes if there is nothing to
// strip).
//
// Why this exists: OpenAI reasoning `encrypted_content` is bound to the account
// that produced it. When the Responses failover loop switches to a freshly
// selected account (sticky drift, 429, account unavailable, ...), the carried
// `encrypted_content` from the previous account cannot be verified by the new
// one, so the upstream returns 400 `invalid_encrypted_content`. The per-account
// HTTP path already recovers from this (see trimOpenAIEncryptedReasoningItems
// callers in openai_gateway_service.go), but only after eating that 400 and
// retrying — one wasted upstream round-trip plus an ops-noise log per account
// switch. Stripping proactively at the handler's failover boundary skips the
// doomed first hop entirely.
//
// This MUST only be applied when an account switch has actually happened. On the
// first (sticky-preferred) account the carried `encrypted_content` is normally
// valid, and stripping it there would needlessly discard reasoning context /
// prompt-cache affinity. Callers gate on len(failedAccountIDs) > 0.
//
// Fast path: if the body does not contain the `encrypted_content` marker it is
// returned untouched with no JSON parse. The re-marshal here is safe — this is
// an OpenAI Responses body, not an Anthropic Messages body, so it carries no
// `thinking` block signature that re-serialization could invalidate (the
// thinking-signature byte-passthrough invariant lives on the native Anthropic
// path, not here). It mirrors exactly the json.Marshal(reqBody) the per-account
// invalid_encrypted_content retry already performs.
func TkStripEncryptedReasoningForFailover(body []byte) []byte {
	if len(body) == 0 || !bytes.Contains(body, encryptedContentMarker) {
		return body
	}

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		// Malformed/non-object body: leave it to the upstream to reject so we
		// don't mask a real client error. The per-account retry path remains a
		// backstop if it really was the encrypted content.
		return body
	}

	if !trimOpenAIEncryptedReasoningItems(reqBody) {
		return body
	}

	out, err := json.Marshal(reqBody)
	if err != nil {
		return body
	}
	return out
}

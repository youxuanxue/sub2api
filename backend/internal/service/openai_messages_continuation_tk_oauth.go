package service

// openAICompatContinuationAllowedAccountType returns true for the account types
// that support previous_response_id continuation on the /v1/messages HTTP path.
//
// Why a TK companion instead of an inline check in openai_messages_continuation.go:
// that file is upstream-shaped (lyen1688, 0584305e). Putting TK account-type
// decisions there would be silently lost on any upstream rewrite of
// openAICompatContinuationEnabled. The sentinel entry in
// engine-facade-sentinels.json (openai_compat_continuation_companion) guards this
// function's existence, making the protection visible to the upstream-merge CI gate.
func openAICompatContinuationAllowedAccountType(account *Account) bool {
	if account == nil {
		return false
	}
	// AccountTypeAPIKey — direct OpenAI Platform Responses API; previous_response_id
	// is the primary stateful-session mechanism.
	// AccountTypeOAuth — ChatGPT Codex endpoint; also supports previous_response_id
	// (falls back gracefully via disableOpenAICompatSessionContinuation on 400).
	return account.Type == AccountTypeAPIKey || account.Type == AccountTypeOAuth
}

// openAICompatShouldTrimForContinuation returns true when the request input
// should be trimmed to the latest user turn before attaching previous_response_id.
//
// Trimming is correct for AccountTypeAPIKey: the Responses API server holds the
// full history, so only the new input is needed.
//
// Trimming is WRONG for AccountTypeOAuth: the ChatGPT Codex path expects full
// replay so the upstream prompt cache can grow turn-by-turn (see
// openai_gateway_messages.go:83 comment). Trimming strips the role=system item
// that AnthropicToResponses placed at input[0]; the OAuth codex transform then
// cannot extract the system prompt and produces blank instructions.
func openAICompatShouldTrimForContinuation(account *Account) bool {
	return account != nil && account.Type == AccountTypeAPIKey
}

// openAICompatShouldDisableContinuationOnPreviousResponseNotFound returns true
// when a previous_response_not_found fallback should permanently disable
// previous_response_id continuation for the current sticky window.
//
// OAuth (ChatGPT Codex) sessions can reject older response IDs even when
// x-codex-turn-state remains valid. Keeping previous_response_id enabled would
// then trigger the same not_found retry on every turn.
func openAICompatShouldDisableContinuationOnPreviousResponseNotFound(account *Account) bool {
	return account != nil && account.Type == AccountTypeOAuth
}

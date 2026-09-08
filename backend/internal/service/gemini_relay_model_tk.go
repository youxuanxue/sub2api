package service

import "strings"

// resolveGeminiForwardModels separates the final provider model from the model
// used for the current HTTP hop. Antigravity API-key accounts with a base URL
// are edge relays, so the edge must receive the public mapping key and apply
// its own mapping before calling the provider.
func resolveGeminiForwardModels(account *Account, requestedModel string) (mappedModel, requestModel string) {
	mappedModel = requestedModel
	if account != nil && (account.Type == AccountTypeAPIKey || account.Type == AccountTypeServiceAccount) {
		mappedModel = account.GetMappedModel(requestedModel)
	}

	return mappedModel, antigravityRelayRequestModel(account, requestedModel, mappedModel)
}

// Edge relays admit public model IDs and resolve provider-only IDs themselves.
// Keep the provider model separate for route facts and usage attribution.
func antigravityRelayRequestModel(account *Account, requestedModel, mappedModel string) string {
	if account != nil &&
		account.Platform == PlatformAntigravity &&
		account.Type == AccountTypeAPIKey &&
		strings.TrimSpace(account.GetCredential("base_url")) != "" {
		return requestedModel
	}
	return mappedModel
}

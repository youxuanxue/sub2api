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

	requestModel = mappedModel
	if account != nil &&
		account.Platform == PlatformAntigravity &&
		account.Type == AccountTypeAPIKey &&
		strings.TrimSpace(account.GetCredential("base_url")) != "" {
		requestModel = requestedModel
	}
	return mappedModel, requestModel
}

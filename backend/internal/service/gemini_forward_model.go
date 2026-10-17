package service

func resolveGeminiForwardModels(account *Account, requestedModel string) (mappedModel, requestModel string) {
	mappedModel = requestedModel
	if account != nil && (account.Type == AccountTypeAPIKey || account.Type == AccountTypeServiceAccount) {
		mappedModel = account.GetMappedModel(requestedModel)
	}

	return mappedModel, mappedModel
}

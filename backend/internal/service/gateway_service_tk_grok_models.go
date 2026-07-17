package service

// mergeGrokNativeCatalogModels unions the curated grok served set into modelSet
// for grok-platform groups. Native xAI OAuth accounts serve grok-imagine media and
// probed chat ids through dedicated native arms regardless of chat-only
// credentials.model_mapping entries (messages-dispatch aliases, claude→grok maps).
// Without this union, GetAvailableModels / universal routing only see mapping keys
// and Studio-advertised grok-imagine-video 403s with universal_no_entitled_group.
func mergeGrokNativeCatalogModels(platform string, modelSet map[string]struct{}) {
	if platform != PlatformGrok || len(supportedGrokCatalogModels) == 0 || modelSet == nil {
		return
	}
	for id := range supportedGrokCatalogModels {
		modelSet[id] = struct{}{}
	}
}

// grokGroupServesNativeCatalogModel reports whether model is in the curated grok
// native catalog that unrestricted OAuth arms serve without a channel whitelist.
func grokGroupServesNativeCatalogModel(model string) bool {
	if len(supportedGrokCatalogModels) == 0 {
		return universalModelPlatformHint(model) == PlatformGrok
	}
	_, ok := supportedGrokCatalogModels[model]
	return ok
}

// grokAccountServesNativeCatalogModel keeps account-level scheduling aligned
// with GetAvailableModels and universal routing. Grok accounts may carry
// chat-only credentials.model_mapping entries for messages-dispatch aliases;
// those mappings must not hide native grok-imagine media or probed chat models
// from the OpenAI-compat scheduler.
func grokAccountServesNativeCatalogModel(account *Account, model string) bool {
	return account != nil && account.Platform == PlatformGrok && grokGroupServesNativeCatalogModel(model)
}

package service

import "strings"

const tkQianfanOverlayPricingSuffix = ".qianfan"

// tkDeepSeekPeakValleyExcludedModels are overlay owners on Baidu Qianfan list
// pricing, not DeepSeek official direct-API peak-valley windows.
var tkDeepSeekPeakValleyExcludedModels = map[string]struct{}{
	"deepseek-v3.2":          {},
	"deepseek-v3.2-think":    {},
	"deepseek-v4-flash-0731": {},
}

// tkQianfanScopedOverlayModels are client-facing model ids that share a global
// overlay owner but bill at Baidu Qianfan list rates when served from account 90.
var tkQianfanScopedOverlayModels = map[string]struct{}{
	"deepseek-v4-pro":   {},
	"deepseek-v4-flash": {},
}

// tkQianfanScopedBillingModel maps billing to the Qianfan overlay owner when the
// serving account is Baidu Qianfan (ch46). Public catalog and GetModelPricing
// keep the global owner; only usage billing on account 90 switches keys.
func tkQianfanScopedBillingModel(model string, account *Account) string {
	model = strings.TrimSpace(model)
	if model == "" || account == nil || !isNewAPIQianfanAccount(account) {
		return model
	}
	if _, ok := tkQianfanScopedOverlayModels[model]; !ok {
		return model
	}
	scoped := model + tkQianfanOverlayPricingSuffix
	if tkOverlayModelPricing(scoped) != nil {
		return scoped
	}
	return model
}

func tkMapQianfanScopedBillingModels(models []string, account *Account) []string {
	if len(models) == 0 || account == nil {
		return models
	}
	out := make([]string, len(models))
	for i, model := range models {
		out[i] = tkQianfanScopedBillingModel(model, account)
	}
	return out
}

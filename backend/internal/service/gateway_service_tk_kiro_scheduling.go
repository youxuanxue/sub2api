package service

import (
	"strings"
)

const tkKiroNativeModelPenalty = 1000

func tkShouldClearStickyForKiroModelMismatch(account *Account, requestedModel string) bool {
	if account == nil || strings.TrimSpace(requestedModel) == "" {
		return false
	}
	if !tkIsKiroMixedSupply(account) {
		return false
	}
	return !kiroSupportsModel(requestedModel)
}

func tkIsKiroMixedSupply(account *Account) bool {
	if account == nil {
		return false
	}

	return account.IsKiro() && account.IsMixedSchedulingEnabled()
}

// tkKiroSelectionPenalty returns a bounded ranking penalty for mirror
// stubs (and mixed Kiro OAuth) on models outside the Kiro catalog when the model
// is otherwise valid on native Anthropic relays.
func tkKiroSelectionPenalty(account *Account, requestedModel string) int {
	if account == nil || !tkIsKiroMixedSupply(account) {
		return 0
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return 0
	}
	if kiroSupportsModel(requestedModel) {
		return 0
	}
	if !tkIsForwardableAnthropicModelName(requestedModel) {
		return 0
	}
	return tkKiroNativeModelPenalty
}

// computeAnthropicKiroPenalties folds native-first preference into the
// existing saturationPenalty slot when the stub penalty is strictly larger.
func computeAnthropicKiroPenalties(candidates []accountWithLoad, requestedModel string) {
	if len(candidates) == 0 || strings.TrimSpace(requestedModel) == "" {
		return
	}
	for i := range candidates {
		penalty := tkKiroSelectionPenalty(candidates[i].account, requestedModel)
		if penalty > candidates[i].saturationPenalty {
			candidates[i].saturationPenalty = penalty
		}
	}
}

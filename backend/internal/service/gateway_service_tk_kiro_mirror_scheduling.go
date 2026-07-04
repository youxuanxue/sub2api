package service

import (
	"strings"
)

// tkKiroMirrorStubNativeModelPenalty is applied when a prod Anthropic Kiro mirror
// stub cannot serve the requested model but a native Anthropic edge relay in the
// same group can. It sorts the stub after native cc-us* relays so universal keys
// match direct-key scheduling for Fable/Opus 4.1 and other native-only SKUs.
const tkKiroMirrorStubNativeModelPenalty = 1000

// tkShouldClearStickyForKiroMirrorModelMismatch clears sticky bindings that pin
// a session to a Kiro mirror stub for a model only native Anthropic accounts serve.
func tkShouldClearStickyForKiroMirrorModelMismatch(account *Account, requestedModel string) bool {
	if account == nil || strings.TrimSpace(requestedModel) == "" {
		return false
	}
	if !account.IsKiroMirrorStub() {
		return false
	}
	return !kiroMirrorStubSupportsModel(requestedModel)
}

// tkKiroMirrorStubSelectionPenalty returns a bounded ranking penalty for mirror
// stubs on models outside the Kiro catalog when the model is otherwise valid on
// native Anthropic relays.
func tkKiroMirrorStubSelectionPenalty(account *Account, requestedModel string) int {
	if account == nil || !account.IsKiroMirrorStub() {
		return 0
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return 0
	}
	if kiroMirrorStubSupportsModel(requestedModel) {
		return 0
	}
	if !tkIsForwardableAnthropicModelName(requestedModel) {
		return 0
	}
	return tkKiroMirrorStubNativeModelPenalty
}

// computeAnthropicKiroMirrorStubPenalties folds native-first preference into the
// existing saturationPenalty slot when the stub penalty is strictly larger.
func computeAnthropicKiroMirrorStubPenalties(candidates []accountWithLoad, requestedModel string) {
	if len(candidates) == 0 || strings.TrimSpace(requestedModel) == "" {
		return
	}
	for i := range candidates {
		penalty := tkKiroMirrorStubSelectionPenalty(candidates[i].account, requestedModel)
		if penalty > candidates[i].saturationPenalty {
			candidates[i].saturationPenalty = penalty
		}
	}
}

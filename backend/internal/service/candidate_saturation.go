package service

import (
	"context"
	"log/slog"
)

// candidateSaturationState owns the interpretation and scope of live empty-pool
// feedback. Redis owns rolling-window expiry; read failures preserve base order.
// Group selection, account scoring and sticky eviction consume this same view.
type candidateSaturationState struct {
	anthropic AnthropicSaturationCounterCache
	openai    OpenAISaturationCounterCache
	settings  *SettingService
}

func (s *GatewayService) candidateSaturationState() candidateSaturationState {
	state := candidateSaturationState{anthropic: s.tkAnthropicSaturationCounter, settings: s.settingService}
	if s.rateLimitService != nil {
		state.openai = s.rateLimitService.openaiSaturationCounter
	}
	return state
}

func (s *OpenAIGatewayService) candidateSaturationState() candidateSaturationState {
	state := candidateSaturationState{openai: s.tkOpenAISaturationCounter, settings: s.settingService}
	if s.rateLimitService != nil {
		state.anthropic = s.rateLimitService.anthropicSaturationCounter
	}
	return state
}

func (s candidateSaturationState) counts(ctx context.Context, accounts []*Account, _ string) map[int64]int64 {
	var anthropicIDs, openaiIDs, nvidiaIDs []int64
	for _, account := range accounts {
		if account == nil {
			continue
		}
		switch {
		case account.Platform == PlatformAnthropic:
			anthropicIDs = append(anthropicIDs, account.ID)
		case isNewAPINVIDIABuildAccount(account):
			nvidiaIDs = append(nvidiaIDs, account.ID)
		case eligibleForOpenAICapacitySaturationPreference(account):
			openaiIDs = append(openaiIDs, account.ID)
		}
	}
	result := make(map[int64]int64)
	merge := func(counts map[int64]int64, err error) {
		if err != nil {
			slog.Warn("candidate_saturation_read_failed", "error", err)
			return
		}
		for id, count := range counts {
			result[id] = count
		}
	}
	if len(anthropicIDs) > 0 && s.anthropic != nil && (s.settings == nil || s.settings.IsAnthropicSaturatedStubDeprioritizeEnabled(ctx)) {
		merge(s.anthropic.GetSaturationBatch(ctx, anthropicIDs, saturationWindowSeconds))
	}
	if len(openaiIDs) > 0 && s.openai != nil && (s.settings == nil || s.settings.IsOpenAISaturatedStubDeprioritizeEnabled(ctx)) {
		merge(s.openai.GetSaturationBatch(ctx, openaiIDs, saturationWindowSeconds))
	}
	if len(nvidiaIDs) > 0 && s.openai != nil {
		merge(s.openai.GetSaturationBatch(ctx, nvidiaIDs, nvidiaBuildInstabilityWindowSeconds))
	}
	return result
}

func candidateSaturated(count int64) bool {
	return count >= saturationThreshold
}

func candidateEffectivePriority(account *Account, counts map[int64]int64) int {
	priority := account.Priority
	count := counts[account.ID]
	if candidateSaturatedFor(account, count) {
		priority += saturationPriorityPenalty + int(count)*100
	}
	return priority
}

func candidateSelectionEffectivePriority(account *Account, counts, failureCounts map[int64]int64) int {
	priority := candidateEffectivePriority(account, counts)
	if failureCounts[account.ID] >= candidateFailureThreshold {
		priority += saturationPriorityPenalty
	}
	return priority
}

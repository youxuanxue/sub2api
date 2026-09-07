package service

import (
	"context"
	"log/slog"
)

// candidateSaturationState owns the interpretation and scope of live empty-pool
// feedback. Redis owns fixed-window expiry; read failures preserve base order.
// Group selection, account scoring and sticky eviction consume this same view.
type candidateSaturationState struct {
	anthropic   AnthropicSaturationCounterCache
	openai      OpenAISaturationCounterCache
	antigravity AntigravitySaturationCounterCache
	settings    *SettingService
}

func (s *GatewayService) candidateSaturationState() candidateSaturationState {
	state := candidateSaturationState{anthropic: s.tkAnthropicSaturationCounter, settings: s.settingService}
	if s.rateLimitService != nil {
		state.openai = s.rateLimitService.openaiSaturationCounter
		state.antigravity = s.rateLimitService.antigravitySaturationCounter
	}
	return state
}

func (s *OpenAIGatewayService) candidateSaturationState() candidateSaturationState {
	state := candidateSaturationState{openai: s.tkOpenAISaturationCounter, settings: s.settingService}
	if s.rateLimitService != nil {
		state.anthropic = s.rateLimitService.anthropicSaturationCounter
		state.antigravity = s.rateLimitService.antigravitySaturationCounter
	}
	return state
}

func (s candidateSaturationState) counts(ctx context.Context, accounts []*Account, model string) map[int64]int64 {
	if model == "" {
		if request, ok := ProtocolRoutingRequest(ctx); ok {
			model = request.RequestedModel()
		}
	}
	var anthropicIDs, openaiIDs []int64
	var scopes []AntigravitySaturationScope
	for _, account := range accounts {
		if account == nil {
			continue
		}
		switch {
		case account.Platform == PlatformAnthropic:
			anthropicIDs = append(anthropicIDs, account.ID)
		case tkIsAntigravityEdgeRelayStub(account) && model != "":
			scopes = append(scopes, AntigravitySaturationScope{account.ID, resolveFinalAntigravityModelKey(ctx, account, model)})
		case tkIsOpenAICompatEdgeMirrorStub(account):
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
		merge(s.anthropic.GetSaturationBatch(ctx, anthropicIDs))
	}
	if len(openaiIDs) > 0 && s.openai != nil && (s.settings == nil || s.settings.IsOpenAISaturatedStubDeprioritizeEnabled(ctx)) {
		merge(s.openai.GetSaturationBatch(ctx, openaiIDs))
	}
	if reader, ok := s.antigravity.(AntigravitySaturationReader); ok && len(scopes) > 0 {
		counts, err := reader.GetSaturationBatch(ctx, scopes)
		if err != nil {
			slog.Warn("candidate_saturation_read_failed", "error", err)
		} else {
			for scope, count := range counts {
				result[scope.AccountID] = count
			}
		}
	}
	return result
}

func candidateSaturated(count int64) bool {
	return count >= edgeMirrorStubSaturationThreshold
}

func candidateEffectivePriority(account *Account, counts map[int64]int64) int {
	priority := account.Priority
	if candidateSaturated(counts[account.ID]) {
		priority += anthropicSaturationPriorityPenalty
	}
	return priority
}

package service

import (
	"context"
)

// Threshold + penalty: edge_mirror_stub_saturation_tk.go (SSOT).

func (s *OpenAIGatewayService) SetOpenAISaturationCounter(cache OpenAISaturationCounterCache) {
	if s != nil {
		s.tkOpenAISaturationCounter = cache
	}
}

func (s *OpenAIGatewayService) HasOpenAISaturationCounter() bool {
	return s != nil && s.tkOpenAISaturationCounter != nil
}

func (s *OpenAIGatewayService) computeOpenAISaturationPenalties(ctx context.Context, candidates []openAIAccountCandidateScore, requestedModel ...string) {
	if s == nil {
		return
	}
	accounts := make([]*Account, 0, len(candidates))
	for _, candidate := range candidates {
		accounts = append(accounts, candidate.account)
	}
	counts := s.candidateSaturationState().counts(ctx, accounts, firstRequestedModel(requestedModel))
	for i := range candidates {
		if candidates[i].account != nil && candidateSaturated(counts[candidates[i].account.ID]) {
			candidates[i].saturationScorePenalty = openAISaturationScorePenalty
		}
	}
}

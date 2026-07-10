package service

import (
	"context"
	"log/slog"
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

func (s *OpenAIGatewayService) computeOpenAISaturationPenalties(ctx context.Context, candidates []openAIAccountCandidateScore) {
	if s == nil || s.tkOpenAISaturationCounter == nil || len(candidates) == 0 {
		return
	}
	if s.settingService != nil && !s.settingService.IsOpenAISaturatedStubDeprioritizeEnabled(ctx) {
		return
	}

	ids := make([]int64, 0, len(candidates))
	for i := range candidates {
		acc := candidates[i].account
		if tkIsOpenAICompatEdgeMirrorStub(acc) {
			ids = append(ids, acc.ID)
		}
	}
	if len(ids) == 0 {
		return
	}

	counts, err := s.tkOpenAISaturationCounter.GetSaturationBatch(ctx, ids)
	if err != nil {
		slog.Warn("openai_saturation_penalty_read_failed", "error", err)
		return
	}
	if len(counts) == 0 {
		return
	}

	var penalized []int64
	for i := range candidates {
		acc := candidates[i].account
		if !tkIsOpenAICompatEdgeMirrorStub(acc) {
			continue
		}
		if counts[acc.ID] >= openAIEdgeMirrorStubSaturationThreshold {
			candidates[i].saturationScorePenalty = openAISaturationScorePenalty
			penalized = append(penalized, acc.ID)
		}
	}
	if len(penalized) > 0 {
		slog.Debug("openai_saturation_penalty_applied",
			"account_ids", penalized,
			"penalty", openAISaturationScorePenalty,
			"candidate_count", len(candidates))
	}
}

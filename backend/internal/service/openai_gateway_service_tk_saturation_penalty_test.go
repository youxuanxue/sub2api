//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func openAIEdgeCandidate(id int64, score float64) openAIAccountCandidateScore {
	return openAIAccountCandidateScore{
		account: openAIEdgeStub(id),
		score:   score,
	}
}

func grokEdgeCandidate(id int64, score float64) openAIAccountCandidateScore {
	return openAIAccountCandidateScore{
		account: grokEdgeStub(id),
		score:   score,
	}
}

func TestComputeOpenAISaturationPenalties_DeprioritizesSaturatedStub(t *testing.T) {
	resetOpenAISatCache()
	svc := &OpenAIGatewayService{}
	svc.SetOpenAISaturationCounter(&fakeSaturationCache{counts: map[int64]int64{
		63: openAIEdgeMirrorStubSaturationThreshold,
		68: openAIEdgeMirrorStubSaturationThreshold - 1,
	}})

	candidates := []openAIAccountCandidateScore{
		openAIEdgeCandidate(63, 3.0),
		openAIEdgeCandidate(68, 2.0),
	}
	svc.computeOpenAISaturationPenalties(context.Background(), candidates)
	require.Equal(t, openAISaturationScorePenalty+float64(openAIEdgeMirrorStubSaturationThreshold), candidates[0].saturationScorePenalty)
	require.Equal(t, 0.0, candidates[1].saturationScorePenalty)
	require.Less(t, candidates[0].score-candidates[0].saturationScorePenalty, candidates[1].score)
}

func TestComputeOpenAISaturationPenalties_DeprioritizesGrokRelayStub(t *testing.T) {
	resetOpenAISatCache()
	svc := &OpenAIGatewayService{}
	svc.SetOpenAISaturationCounter(&fakeSaturationCache{counts: map[int64]int64{
		80: openAIEdgeMirrorStubSaturationThreshold,
	}})

	candidates := []openAIAccountCandidateScore{
		grokEdgeCandidate(80, 3.0),
		grokEdgeCandidate(81, 2.0),
	}
	svc.computeOpenAISaturationPenalties(context.Background(), candidates)
	require.Equal(t, openAISaturationScorePenalty+float64(openAIEdgeMirrorStubSaturationThreshold), candidates[0].saturationScorePenalty)
	require.Equal(t, 0.0, candidates[1].saturationScorePenalty)
	require.Less(t, candidates[0].score-candidates[0].saturationScorePenalty, candidates[1].score)
}

func TestComputeOpenAISaturationPenalties_KillSwitchOff(t *testing.T) {
	resetOpenAISatCache()
	svc := &OpenAIGatewayService{}
	svc.SetOpenAISaturationCounter(&fakeSaturationCache{counts: map[int64]int64{63: 100}})
	svc.settingService = NewSettingService(
		&satSettingRepoStub{values: map[string]string{
			SettingKeyOpenAISaturatedStubDeprioritizeEnabled: "false",
		}},
		&config.Config{},
	)
	candidates := []openAIAccountCandidateScore{openAIEdgeCandidate(63, 3.0)}
	svc.computeOpenAISaturationPenalties(context.Background(), candidates)
	require.Equal(t, 0.0, candidates[0].saturationScorePenalty)
	resetOpenAISatCache()
}

// Chat/completions historically force UseUpstreamTokenCost=true. Saturation
// preference must still sink a saturated edge stub behind a healthy peer.
func TestBuildOpenAIAccountLoadPlan_SaturationAppliesWithUpstreamTokenCost(t *testing.T) {
	resetOpenAISatCache()
	cfg := &config.Config{}
	// TopK=1 makes preference fatal: saturated stub must not occupy the only slot.
	cfg.Gateway.OpenAIWS.LBTopK = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1
	svc := &OpenAIGatewayService{cfg: cfg}
	svc.SetOpenAISaturationCounter(&fakeSaturationCache{counts: map[int64]int64{
		63: openAIEdgeMirrorStubSaturationThreshold,
	}})
	scheduler := &defaultOpenAIAccountScheduler{service: svc}

	saturated := openAIEdgeStub(63)
	saturated.Concurrency = 30
	saturated.Priority = 1
	healthy := openAIEdgeStub(68)
	healthy.Concurrency = 80
	healthy.Priority = 1

	loadMap := map[int64]*AccountLoadInfo{
		// Idle dead stub would otherwise win on loadFactor without the penalty.
		63: {AccountID: 63, LoadRate: 0, CurrentConcurrency: 0},
		68: {AccountID: 68, LoadRate: 80, CurrentConcurrency: 64},
	}
	plan := scheduler.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{
		UseUpstreamTokenCost: true,
		RequestedModel:       "gpt-5.6-luna",
	}, []*Account{saturated, healthy}, loadMap)

	require.Len(t, plan.candidates, 2)
	var satScore, healthyScore float64
	for _, c := range plan.candidates {
		switch c.account.ID {
		case 63:
			require.Greater(t, c.saturationScorePenalty, 0.0)
			satScore = c.score
		case 68:
			require.Equal(t, 0.0, c.saturationScorePenalty)
			healthyScore = c.score
		}
	}
	require.Less(t, satScore, healthyScore, "saturated idle stub must score below busy healthy peer even with UseUpstreamTokenCost")
	require.Len(t, plan.selectionOrder, 1)
	require.Equal(t, int64(68), plan.selectionOrder[0].account.ID,
		"TopK=1 acquire candidate must be the healthy peer, not the saturated stub")
	resetOpenAISatCache()
}

func resetOpenAISatCache() {
	openaiSatDeprioritizeCache.Store((*tkOptOutFlagCacheEntry)(nil))
}

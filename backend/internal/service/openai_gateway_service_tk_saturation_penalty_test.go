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

func TestComputeOpenAISaturationPenalties_DeprioritizesSaturatedStub(t *testing.T) {
	resetOpenAISatCache()
	svc := &OpenAIGatewayService{}
	svc.SetOpenAISaturationCounter(&fakeSaturationCache{counts: map[int64]int64{
		63: anthropicSaturationThreshold,
		68: anthropicSaturationThreshold - 1,
	}})

	candidates := []openAIAccountCandidateScore{
		openAIEdgeCandidate(63, 3.0),
		openAIEdgeCandidate(68, 2.0),
	}
	svc.computeOpenAISaturationPenalties(context.Background(), candidates)
	require.Equal(t, openAISaturationScorePenalty, candidates[0].saturationScorePenalty)
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

func resetOpenAISatCache() {
	openaiSatDeprioritizeCache.Store((*openaiSatDeprioritizeCacheEntry)(nil))
}

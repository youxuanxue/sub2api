//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCandidateSaturatedFor_NVIDIAThresholdOne(t *testing.T) {
	t.Parallel()
	nvidia := &Account{
		ID: 138, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": newapiintegration.NVIDIABuildBaseURL},
	}
	ordinary := newNewAPIBridgeAccount()
	require.True(t, candidateSaturatedFor(nvidia, 1), "first NVIDIA error must deprioritize")
	require.False(t, candidateSaturatedFor(ordinary, 1), "ordinary bridge keeps threshold-5")
	require.False(t, candidateSaturatedFor(ordinary, edgeMirrorStubSaturationThreshold-1))
	require.True(t, candidateSaturatedFor(ordinary, edgeMirrorStubSaturationThreshold))
}

func TestBridgeWrapRelayErrorAfterPenalty_NVIDIARecordsInstability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	counter := &fakeOpenAISaturationCounterRL{}
	rls := &RateLimitService{openaiSaturationCounter: counter}
	nvidia := &Account{
		ID: 138, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": newapiintegration.NVIDIABuildBaseURL},
	}

	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), rls, c, nvidia, upstreamBridgeError(http.StatusInternalServerError, "Failed to generate completions"))
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.True(t, failover.ShouldRetryNextAccount())
	require.Equal(t, []int64{138}, counter.incrementIDs)
	require.Equal(t, nvidiaBuildInstabilityWindowSeconds, counter.lastWindow)

	// Non-NVIDIA account must not touch the NVIDIA instability counter path.
	counter.incrementIDs = nil
	ordinary := newNewAPIBridgeAccount()
	_ = bridgeWrapRelayErrorAfterPenalty(context.Background(), rls, c, ordinary, upstreamBridgeError(http.StatusInternalServerError, "internal error"))
	require.Empty(t, counter.incrementIDs, "ordinary newapi 500 stays terminal without NVIDIA soft penalty")
}

func TestCandidateSaturationState_ReadsNVIDIAWindow(t *testing.T) {
	t.Parallel()
	counter := &fakeOpenAISaturationCounterRL{batch: map[int64]int64{138: 1}}
	state := candidateSaturationState{openai: counter}
	nvidia := &Account{
		ID: 138, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": newapiintegration.NVIDIABuildBaseURL},
	}
	counts := state.counts(context.Background(), []*Account{nvidia}, "glm-5.3-flash")
	require.Equal(t, int64(1), counts[138])
	require.Equal(t, nvidiaBuildInstabilityWindowSeconds, counter.lastWindow)
	require.Equal(t, 1000+100, candidateEffectivePriority(nvidia, counts))
}

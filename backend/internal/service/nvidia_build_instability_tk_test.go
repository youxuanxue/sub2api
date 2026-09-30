//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestRecordNVIDIABuildInstability_NoOpWhenRedisUnwired(t *testing.T) {
	t.Parallel()
	rls := &RateLimitService{} // no openaiSaturationCounter
	require.Zero(t, rls.recordNVIDIABuildInstability(context.Background(), 205, 500),
		"without Redis, soft deprioritize is a no-op; failover still handles the request")
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

func TestNVIDIABuildSupplyRepresentativesMatchWireTargets(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "..", "ops", "stage0", "gateway-account-supply.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc struct {
		Classes []struct {
			Dialect         string `json:"dialect"`
			Representatives []struct {
				Model             string   `json:"model"`
				UpstreamModel     string   `json:"upstream_model"`
				RepresentedModels []string `json:"represented_models"`
			} `json:"representatives"`
		} `json:"classes"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	var found bool
	for _, class := range doc.Classes {
		if class.Dialect != "nvidia-build" {
			continue
		}
		found = true
		require.NotEmpty(t, class.Representatives)
		for _, rep := range class.Representatives {
			target, ok := nvidiaBuildModelTargets[rep.Model]
			require.True(t, ok, "supply representative %q must be a nvidiaBuildModelTargets key", rep.Model)
			require.Equal(t, target, rep.UpstreamModel,
				"supply upstream_model must equal the wire-target owner for %s", rep.Model)
			for _, represented := range rep.RepresentedModels {
				_, ok := nvidiaBuildModelTargets[represented]
				require.True(t, ok, "represented model %q must stay inside nvidiaBuildModelTargets", represented)
			}
		}
	}
	require.True(t, found, "nvidia-build supply class missing")
	manifestIDs := tkServedModelsManifestPresetIDsForSelector(
		PlatformNewAPI, newapiconstant.ChannelTypeOpenAI, newapiintegration.NVIDIABuildBaseURL)
	require.ElementsMatch(t, manifestIDs, mapKeysForNVIDIATest(nvidiaBuildModelTargets),
		"manifest nvidia scopes, wire targets, and supply reps must stay one floor")
}

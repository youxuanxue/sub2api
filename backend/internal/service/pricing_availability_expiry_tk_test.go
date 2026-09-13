//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAvailabilityReadExpiresWithoutNewTraffic(t *testing.T) {
	for _, nativeBatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback", true: "batch"}[nativeBatch], func(t *testing.T) {
			svc, repo, clock := newAvailabilityTestService(t)
			ctx := withModelAvailabilityRequestCache(context.Background())
			outcome := AvailabilityOutcome{Platform: PlatformOpenAI, ModelID: "gpt-4o", AccountID: 1, Success: true, UpstreamStatusCode: 200}
			svc.RecordOutcome(ctx, outcome)
			stored, err := repo.Get(ctx, outcome.Platform, outcome.ModelID)
			require.NoError(t, err)
			var batchRepo *batchAvailabilityRepoStub
			if nativeBatch {
				batchRepo = &batchAvailabilityRepoStub{states: map[string]AvailabilityState{"openai/gpt-4o": stored}}
				svc = NewPricingAvailabilityService(batchRepo, clock.Now)
			}
			check := func(status string, count int) {
				t.Helper()
				single, err := svc.GetAvailability(ctx, outcome.Platform, outcome.ModelID)
				require.NoError(t, err)
				batch, err := svc.GetAvailabilityBatch(ctx, outcome.Platform, []string{outcome.ModelID, "never-observed"})
				require.NoError(t, err)
				require.Equal(t, single, batch[outcome.ModelID])
				require.NotContains(t, batch, "never-observed")
				require.Equal(t, status, single.Status)
				require.Equal(t, count, single.SampleTotal24h)
				require.Equal(t, stored.LastSeenOKAt, single.LastSeenOKAt)
				require.Equal(t, stored.LastCheckedAt, single.LastCheckedAt)
				require.Equal(t, stored.RollingWindowStartedAt, single.RollingWindowStartedAt)
				catalog := DecorateAndPruneByAvailability(ctx, &PublicCatalogResponse{Data: []PublicCatalogModel{{ModelID: outcome.ModelID, Vendor: "openai"}}}, svc)
				require.Len(t, catalog.Data, 1)
				require.Equal(t, status, catalog.Data[0].Availability.Status)
				require.Equal(t, count, catalog.Data[0].Availability.SampleCount24h)
				if count == 0 {
					require.Zero(t, single.SuccessRate24h())
					require.Zero(t, catalog.Data[0].Availability.SuccessRate24h)
				}
			}
			check(AvailabilityStatusOK, 1) // Populates the request-local cache.
			clock.Advance(AvailabilityRollingWindow - time.Nanosecond)
			check(AvailabilityStatusOK, 1)
			clock.Advance(time.Nanosecond)
			check(AvailabilityStatusStale, 0)
			clock.Advance(6 * 24 * time.Hour)
			check(AvailabilityStatusStale, 0)
			if nativeBatch {
				require.Equal(t, 1, batchRepo.batchCalls, "expiration must not require requerying cached evidence")
				require.Equal(t, stored, batchRepo.states["openai/gpt-4o"])
			}
			after, err := repo.Get(ctx, outcome.Platform, outcome.ModelID)
			require.NoError(t, err)
			require.Equal(t, stored, after, "reads must not rewrite evidence")
		})
	}
}

func TestAvailabilityReadExpiresFailuresAndRetirementProof(t *testing.T) {
	for _, kind := range []string{"transient", "first-attestation", "confirmed-retirement", "auth-only", "never-observed"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			svc, _, clock := newAvailabilityTestService(t)
			outcome := AvailabilityOutcome{Platform: PlatformOpenAI, ModelID: "gpt-4o", UpstreamStatusCode: 503}
			switch kind {
			case "first-attestation", "confirmed-retirement":
				outcome.ProviderModelRetired = true
				outcome.UpstreamStatusCode = 404
				outcome.UpstreamErrorBody = "model retired"
			case "auth-only":
				outcome.UpstreamStatusCode = 401
			}
			if kind != "never-observed" {
				svc.RecordOutcome(ctx, outcome)
			}
			if kind == "confirmed-retirement" {
				clock.Advance(time.Minute)
				svc.RecordOutcome(ctx, outcome)
			}
			require.Equal(t, kind == "confirmed-retirement", svc.IsStructurallyGone(ctx, outcome.Platform, outcome.ModelID))
			if kind == "first-attestation" {
				state, err := svc.GetAvailability(ctx, outcome.Platform, outcome.ModelID)
				require.NoError(t, err)
				require.Equal(t, AvailabilityStatusStale, state.Status, "reads must not confirm a first attestation")
			}
			clock.Advance(AvailabilityRollingWindow)
			state, err := svc.GetAvailability(ctx, outcome.Platform, outcome.ModelID)
			require.NoError(t, err)
			require.Zero(t, state.SampleTotal24h)
			require.False(t, svc.IsStructurallyGone(ctx, outcome.Platform, outcome.ModelID))
			if kind == "never-observed" {
				require.Equal(t, AvailabilityState{}, state)
			} else {
				require.Equal(t, AvailabilityStatusStale, state.Status)
				require.NotNil(t, state.LastFailureAt)
			}
		})
	}
}

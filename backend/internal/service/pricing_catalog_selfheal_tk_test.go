//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TK (us7 P0 2026-06-13): the catalog self-heal. A model the upstream rejects as
// not-found (model_not_found → unreachable) is "structurally gone" and must drop
// from the servable surfaces (public /pricing + Your-Menu) WITHOUT a manual
// allowlist edit; a model with TRANSIENT trouble (5xx/network) is "degraded" and
// must STAY listed with its badge so the storefront does not flap.
func TestTkAvailabilityStructurallyGone(t *testing.T) {
	require.True(t, tkAvailabilityStructurallyGone(AvailabilityState{
		Status: AvailabilityStatusUnreachable, LastFailureKind: FailureKindModelNotFound,
	}), "unreachable via model_not_found = gone")

	require.False(t, tkAvailabilityStructurallyGone(AvailabilityState{
		Status: AvailabilityStatusUnreachable, LastFailureKind: FailureKindUpstream5xx,
	}), "unreachable via 5xx = degraded, NOT gone (keep with badge)")

	require.False(t, tkAvailabilityStructurallyGone(AvailabilityState{
		Status: AvailabilityStatusOK, LastFailureKind: "",
	}), "ok = keep")

	require.False(t, tkAvailabilityStructurallyGone(AvailabilityState{}),
		"untested (zero value) = keep, never hide an unprobed model")

	require.False(t, tkAvailabilityStructurallyGone(AvailabilityState{
		Status: AvailabilityStatusStale, LastFailureKind: FailureKindModelNotFound,
	}), "stale (not unreachable) = keep — only a current unreachable hides")
}

func seedAvail(repo *memoryAvailabilityRepo, platform, modelID, status, kind string) {
	repo.rows[repo.key(platform, modelID)] = AvailabilityState{
		Platform: platform, ModelID: modelID, Status: status, LastFailureKind: kind,
	}
}

func TestDecorateAndPruneByAvailability(t *testing.T) {
	svc, repo, _ := newAvailabilityTestService(t)
	seedAvail(repo, PlatformAnthropic, "claude-fable-5", AvailabilityStatusUnreachable, FailureKindModelNotFound) // gone
	seedAvail(repo, PlatformAnthropic, "claude-opus-4-8", AvailabilityStatusUnreachable, FailureKindUpstream5xx)  // degraded
	// claude-sonnet-4-6 unseeded → untested → keep, no badge drop.

	resp := &PublicCatalogResponse{Object: "list", Data: []PublicCatalogModel{
		{ModelID: "claude-fable-5", Vendor: "anthropic"},
		{ModelID: "claude-opus-4-8", Vendor: "anthropic"},
		{ModelID: "claude-sonnet-4-6", Vendor: "anthropic"},
		{ModelID: "deepseek-chat", Vendor: "deepseek"}, // unknown platform → pass through untouched
	}}

	out := DecorateAndPruneByAvailability(context.Background(), resp, svc)

	got := map[string]*PublicCatalogModel{}
	for i := range out.Data {
		got[out.Data[i].ModelID] = &out.Data[i]
	}
	require.NotContains(t, got, "claude-fable-5", "structurally-gone model must be hidden from the storefront")
	require.Contains(t, got, "claude-opus-4-8", "degraded model stays listed")
	require.Equal(t, AvailabilityStatusUnreachable, got["claude-opus-4-8"].Availability.Status, "degraded model keeps its badge")
	require.Contains(t, got, "claude-sonnet-4-6", "untested model stays listed")
	require.Contains(t, got, "deepseek-chat", "unknown-platform model passes through")

	// nil-safe: svc == nil returns resp unchanged (no pruning).
	require.Len(t, DecorateAndPruneByAvailability(context.Background(), resp, nil).Data, 4)
}

func TestMePricingPruneStructurallyGoneIDs(t *testing.T) {
	svc, repo, _ := newAvailabilityTestService(t)
	seedAvail(repo, PlatformAnthropic, "claude-fable-5", AvailabilityStatusUnreachable, FailureKindModelNotFound)
	seedAvail(repo, PlatformAnthropic, "claude-opus-4-8", AvailabilityStatusUnreachable, FailureKindUpstream5xx)

	// *PricingAvailabilityService satisfies MePricingAvailability.
	mps := &MePricingCatalogService{availability: svc}
	got := mps.pruneStructurallyGoneIDs(context.Background(), PlatformAnthropic,
		[]string{"claude-fable-5", "claude-opus-4-8", "claude-sonnet-4-6"})
	require.Equal(t, []string{"claude-opus-4-8", "claude-sonnet-4-6"}, got,
		"only the structurally-gone fable-5 is pruned from the menu fallback")

	// nil availability → passthrough (Phase-1 / tests).
	none := &MePricingCatalogService{}
	require.Len(t, none.pruneStructurallyGoneIDs(context.Background(), PlatformAnthropic, []string{"a", "b"}), 2)
}

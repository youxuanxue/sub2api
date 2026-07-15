//go:build unit

package service

import (
	"context"
	"sort"
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
	anthropic := firstNPlatformServableIDsForSelfHealTest(t, PlatformAnthropic, 3)
	gone, degraded, untested := anthropic[0], anthropic[1], anthropic[2]
	seedAvail(repo, PlatformAnthropic, gone, AvailabilityStatusUnreachable, FailureKindModelNotFound)
	seedAvail(repo, PlatformAnthropic, degraded, AvailabilityStatusUnreachable, FailureKindUpstream5xx)

	resp := &PublicCatalogResponse{Object: "list", Data: []PublicCatalogModel{
		{ModelID: gone, Vendor: "anthropic"},
		{ModelID: degraded, Vendor: "anthropic"},
		{ModelID: untested, Vendor: "anthropic"},
		{ModelID: "custom-not-native-zzz", Vendor: "custom-vendor"}, // unknown platform → pass through untouched
	}}

	out := DecorateAndPruneByAvailability(context.Background(), resp, svc)

	got := map[string]*PublicCatalogModel{}
	for i := range out.Data {
		got[out.Data[i].ModelID] = &out.Data[i]
	}
	require.NotContains(t, got, gone, "structurally-gone model must be hidden from the storefront")
	require.Contains(t, got, degraded, "degraded model stays listed")
	require.Equal(t, AvailabilityStatusUnreachable, got[degraded].Availability.Status, "degraded model keeps its badge")
	require.Contains(t, got, untested, "untested model stays listed")
	require.Contains(t, got, "custom-not-native-zzz", "unknown-platform model passes through")

	// nil-safe: svc == nil returns resp unchanged (no pruning).
	require.Len(t, DecorateAndPruneByAvailability(context.Background(), resp, nil).Data, 4)
}

func TestMePricingPruneStructurallyGoneIDs(t *testing.T) {
	svc, repo, _ := newAvailabilityTestService(t)
	anthropic := firstNPlatformServableIDsForSelfHealTest(t, PlatformAnthropic, 3)
	gone, degraded, untested := anthropic[0], anthropic[1], anthropic[2]
	seedAvail(repo, PlatformAnthropic, gone, AvailabilityStatusUnreachable, FailureKindModelNotFound)
	seedAvail(repo, PlatformAnthropic, degraded, AvailabilityStatusUnreachable, FailureKindUpstream5xx)

	// *PricingAvailabilityService satisfies MePricingAvailability.
	mps := &MePricingCatalogService{availability: svc}
	got := mps.pruneStructurallyGoneIDs(context.Background(), PlatformAnthropic,
		[]string{gone, degraded, untested})
	require.Equal(t, []string{degraded, untested}, got,
		"only the structurally-gone model is pruned from the menu fallback")

	// nil availability → passthrough (Phase-1 / tests).
	none := &MePricingCatalogService{}
	require.Len(t, none.pruneStructurallyGoneIDs(context.Background(), PlatformAnthropic, []string{"a", "b"}), 2)
}

func firstNPlatformServableIDsForSelfHealTest(t *testing.T, platform string, n int) []string {
	t.Helper()
	ids := supportedCatalogModelIDsForPlatform(platform)
	sort.Strings(ids)
	require.GreaterOrEqual(t, len(ids), n, "platform %s SSOT must have enough ids for this test", platform)
	return append([]string{}, ids[:n]...)
}

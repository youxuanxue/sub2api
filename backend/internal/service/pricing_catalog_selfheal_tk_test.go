//go:build unit

package service

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TK (us7 P0 2026-06-13): the catalog self-heal. A model the upstream rejects as
// not-found (provider_model_retired → unreachable) is "structurally gone" and must drop
// from the servable surfaces (public /pricing + Your-Menu) WITHOUT a manual
// allowlist edit; a model with TRANSIENT trouble (5xx/network) is "degraded" and
// must STAY listed with its badge so the storefront does not flap.
func TestTkAvailabilityStructurallyGone(t *testing.T) {
	require.True(t, tkAvailabilityStructurallyGone(AvailabilityState{
		Status: AvailabilityStatusUnreachable, LastFailureKind: FailureKindProviderModelRetired,
	}), "confirmed provider retirement = gone")

	require.False(t, tkAvailabilityStructurallyGone(AvailabilityState{
		Status: AvailabilityStatusUnreachable, LastFailureKind: FailureKindUpstream5xx,
	}), "unreachable via 5xx = degraded, NOT gone (keep with badge)")

	require.False(t, tkAvailabilityStructurallyGone(AvailabilityState{
		Status: AvailabilityStatusOK, LastFailureKind: "",
	}), "ok = keep")

	require.False(t, tkAvailabilityStructurallyGone(AvailabilityState{}),
		"untested (zero value) = keep, never hide an unprobed model")

	require.False(t, tkAvailabilityStructurallyGone(AvailabilityState{
		Status: AvailabilityStatusStale, LastFailureKind: FailureKindProviderModelRetired,
	}), "stale (not unreachable) = keep — only a current unreachable hides")
}

func seedAvail(repo *memoryAvailabilityRepo, platform, modelID, status, kind string, now time.Time) {
	repo.rows[repo.key(platform, modelID)] = AvailabilityState{
		Platform: platform, ModelID: modelID, Status: status, LastFailureKind: kind,
		LastFailureAt: &now, LastCheckedAt: &now, RollingWindowStartedAt: &now, SampleTotal24h: 1,
	}
}

func TestDecorateAndPruneByAvailability(t *testing.T) {
	svc, repo, _ := newAvailabilityTestService(t)
	anthropic := firstNPlatformServableIDsForSelfHealTest(t, PlatformAnthropic, 3)
	gone, degraded, untested := anthropic[0], anthropic[1], anthropic[2]
	seedAvail(repo, PlatformAnthropic, gone, AvailabilityStatusUnreachable, FailureKindProviderModelRetired, svc.clock())
	seedAvail(repo, PlatformAnthropic, degraded, AvailabilityStatusUnreachable, FailureKindUpstream5xx, svc.clock())

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
	seedAvail(repo, PlatformAnthropic, gone, AvailabilityStatusUnreachable, FailureKindProviderModelRetired, svc.clock())
	seedAvail(repo, PlatformAnthropic, degraded, AvailabilityStatusUnreachable, FailureKindUpstream5xx, svc.clock())

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

// The public surface must use the same batch evidence owner as discovery.
type catalogBatchEvidenceRepo struct {
	states       map[string]map[string]AvailabilityState
	calls        map[string]int
	failPlatform string
}

func (r *catalogBatchEvidenceRepo) Get(context.Context, string, string) (AvailabilityState, error) {
	return AvailabilityState{}, errors.New("unexpected single read")
}
func (r *catalogBatchEvidenceRepo) Upsert(context.Context, string, string, func(AvailabilityState) AvailabilityState) error {
	return errors.New("read only")
}
func (r *catalogBatchEvidenceRepo) GetBatch(_ context.Context, platform string, _ []string) (map[string]AvailabilityState, error) {
	r.calls[platform]++
	if platform == r.failPlatform {
		return nil, errors.New("unavailable")
	}
	return r.states[platform], nil
}
func TestPublicCatalogBatchEvidencePreservesProjectionAndFailures(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	current := AvailabilityState{Platform: PlatformAnthropic, ModelID: "gone", Status: AvailabilityStatusUnreachable, LastFailureKind: FailureKindProviderModelRetired, LastFailureAt: &now, RollingWindowStartedAt: &now, SampleTotal24h: 2}
	expired := current
	expired.ModelID = "expired"
	expired.LastFailureAt = &old
	expired.RollingWindowStartedAt = &old
	repo := &catalogBatchEvidenceRepo{calls: map[string]int{}, failPlatform: PlatformOpenAI, states: map[string]map[string]AvailabilityState{PlatformAnthropic: {"gone": current, "expired": expired}}}
	resp := &PublicCatalogResponse{Object: "list", UpdatedAt: now, Data: []PublicCatalogModel{
		{ModelID: "gone", Vendor: "anthropic"}, {ModelID: "expired", Vendor: "anthropic"}, {ModelID: "unknown", Vendor: "anthropic"},
		{ModelID: "failed", Vendor: "openai"}, {ModelID: "vendor", Vendor: "unmapped"}, {ModelID: "expired", Vendor: "anthropic"},
	}}
	out := DecorateAndPruneByAvailability(context.Background(), resp, NewPricingAvailabilityService(repo, func() time.Time { return now }))
	require.Equal(t, map[string]int{PlatformAnthropic: 1, PlatformOpenAI: 1}, repo.calls)
	require.Len(t, out.Data, 5)
	require.Equal(t, resp.UpdatedAt, out.UpdatedAt)
	require.Equal(t, "expired", out.Data[0].ModelID)
	require.Equal(t, AvailabilityStatusStale, out.Data[0].Availability.Status)
	require.Zero(t, out.Data[0].Availability.SampleCount24h)
	require.Equal(t, AvailabilityStatusUntested, out.Data[1].Availability.Status)
	require.Nil(t, out.Data[2].Availability, "failed batch retains rows without badges")
	require.Nil(t, out.Data[3].Availability, "unknown vendor remains untouched")
	require.Equal(t, out.Data[0], out.Data[4], "duplicate rows retain order and identical evidence")
	require.Len(t, resp.Data, 6)
	for _, m := range resp.Data {
		require.Nil(t, m.Availability, "base catalog is immutable")
	}
}

//go:build unit

package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// tkBuildPricedServiceForTest returns a PricingCatalogService that prices exactly
// the given ids (non-zero), so IsModelPriced(id) is true for them and false
// otherwise. Used to exercise the ∩priced half of ServableClientFacingIDs.
func tkBuildPricedServiceForTest(t *testing.T, ids []string) *PricingCatalogService {
	t.Helper()
	entries := make([]string, len(ids))
	for i, id := range ids {
		entries[i] = fmt.Sprintf(`%q:{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"litellm_provider":"test"}`, id)
	}
	data := []byte("{" + strings.Join(entries, ",") + "}")
	svc := NewPricingCatalogService(nil)
	svc.SetSourceForTesting(func() ([]byte, time.Time, bool) { return data, time.Unix(0, 0), true })
	return svc
}

// TestServableClientFacingIDs_InvariantAndAdvertisedDead pins the Goal-1 SSOT
// invariant for the gateway /v1/models fallback source: every advertised id is
// (a) within the platform servable allowlist (the /pricing candidate gate) AND
// (b) priced (billable) — visible ⟹ priced ∧ candidate. Negative pin:
// priced-but-not-allowlisted ids (advertised_dead like gpt-5.2 / gpt-image-1)
// never appear, even when priced.
func TestServableClientFacingIDs_InvariantAndAdvertisedDead(t *testing.T) {
	ctx := context.Background()
	allow := supportedCatalogModelIDsForPlatform(PlatformOpenAI)
	require.NotEmpty(t, allow, "openai allowlist must be populated")
	allowSet := make(map[string]bool, len(allow))
	for _, id := range allow {
		allowSet[id] = true
	}
	dead := []string{"gpt-5.2", "gpt-image-1", "codex-auto-review", "gpt-5.3-codex"}
	for _, d := range dead {
		require.False(t, allowSet[d], "precondition: %s must be advertised_dead (priced but NOT in allowlist)", d)
	}
	// Price EVERYTHING (allowlist + dead ids) so the ONLY thing that can keep a
	// dead id out is the candidate (allowlist) gate, not the price gate.
	pricing := tkBuildPricedServiceForTest(t, append(append([]string{}, allow...), dead...))

	got := ServableClientFacingIDs(ctx, PlatformOpenAI, nil, pricing)
	require.NotEmpty(t, got)
	gotSet := make(map[string]bool, len(got))
	for _, id := range got {
		gotSet[id] = true
		require.True(t, allowSet[id], "%s leaked outside the openai servable allowlist (candidate gate broken)", id)
		require.True(t, pricing.IsModelPriced(id, PlatformOpenAI), "%s visible but not priced (invariant broken)", id)
	}
	for _, d := range dead {
		require.False(t, gotSet[d], "advertised_dead %s must not reach the /v1/models fallback", d)
	}
}

// TestServableClientFacingIDs_DropsVisibleButUnpriced pins the other half of the
// invariant: an id IN the servable allowlist but WITHOUT a price is dropped (never
// advertised at $0). Reproduces the tab_flash_lite_preview class structurally.
func TestServableClientFacingIDs_DropsVisibleButUnpriced(t *testing.T) {
	ctx := context.Background()
	const probe = "zzz-unpriced-probe-model"
	supportedOpenAICatalogModels[probe] = struct{}{}
	defer delete(supportedOpenAICatalogModels, probe)

	allow := supportedCatalogModelIDsForPlatform(PlatformOpenAI)
	priced := make([]string, 0, len(allow))
	for _, id := range allow {
		if id != probe {
			priced = append(priced, id)
		}
	}
	pricing := tkBuildPricedServiceForTest(t, priced)

	gotSet := make(map[string]bool)
	for _, id := range ServableClientFacingIDs(ctx, PlatformOpenAI, nil, pricing) {
		gotSet[id] = true
	}
	require.False(t, gotSet[probe], "allowlisted-but-unpriced id must be dropped by ∩priced (visible ⟹ priced)")
	require.True(t, gotSet["gpt-5"], "a priced allowlist id (gpt-5) must remain")
}

// TestServableClientFacingIDs_PrunesStructurallyGone confirms the unified source
// also drops structurally-gone ids (model_not_found→unreachable), so the
// /v1/models fallback self-heals identically to /pricing and Your-Menu.
func TestServableClientFacingIDs_PrunesStructurallyGone(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newAvailabilityTestService(t)
	seedAvail(repo, PlatformAnthropic, "claude-opus-4-8", AvailabilityStatusUnreachable, FailureKindModelNotFound)
	pricing := tkBuildPricedServiceForTest(t, supportedCatalogModelIDsForPlatform(PlatformAnthropic))
	for _, id := range ServableClientFacingIDs(ctx, PlatformAnthropic, svc, pricing) {
		require.NotEqual(t, "claude-opus-4-8", id, "structurally-gone model must be pruned from the unified servable source")
	}
}

// TK (R-003, follow-up to PR #752): the admin model-whitelist selector now draws
// its candidates from tkServableCandidateIDs (self-healing) instead of the
// canonical defaults. These pin that membership truth on the Go side so the
// frontend no longer hand-maintains a hardcoded mirror.
func TestTkServableCandidateIDs(t *testing.T) {
	ctx := context.Background()

	contains := func(ids []string, want string) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}

	t.Run("anthropic draws from the empirically-servable allowlist (no fable-5 post-#752)", func(t *testing.T) {
		svc, _, _ := newAvailabilityTestService(t)
		ids := tkServableCandidateIDs(ctx, PlatformAnthropic, svc)
		require.True(t, contains(ids, "claude-opus-4-8"), "servable opus present")
		require.False(t, contains(ids, "claude-fable-5"), "access-gated fable-5 absent (W2 removed it from the allowlist)")
	})

	t.Run("structurally-gone model is pruned; degraded model stays", func(t *testing.T) {
		svc, repo, _ := newAvailabilityTestService(t)
		seedAvail(repo, PlatformAnthropic, "claude-opus-4-8", AvailabilityStatusUnreachable, FailureKindModelNotFound) // gone
		seedAvail(repo, PlatformAnthropic, "claude-sonnet-4-6", AvailabilityStatusUnreachable, FailureKindUpstream5xx)  // degraded
		ids := tkServableCandidateIDs(ctx, PlatformAnthropic, svc)
		require.False(t, contains(ids, "claude-opus-4-8"), "model_not_found→unreachable auto-drops (self-heal)")
		require.True(t, contains(ids, "claude-sonnet-4-6"), "transient 5xx-unreachable stays")
	})

	t.Run("antigravity keeps fable-5 (per-platform truth: servable there)", func(t *testing.T) {
		svc, _, _ := newAvailabilityTestService(t)
		ids := tkServableCandidateIDs(ctx, PlatformAntigravity, svc)
		require.True(t, contains(ids, "claude-fable-5"),
			"fable-5 gone on anthropic must NOT vanish from antigravity — the backend is the per-platform authority")
	})

	t.Run("nil availability degrades to no pruning (passthrough)", func(t *testing.T) {
		ids := tkServableCandidateIDs(ctx, PlatformAnthropic, nil)
		require.True(t, contains(ids, "claude-opus-4-8"), "without availability the full allowlist passes through")
	})
}

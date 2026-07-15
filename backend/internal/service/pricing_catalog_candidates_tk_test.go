//go:build unit

package service

import (
	"context"
	"fmt"
	"sort"
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
// priced-but-not-allowlisted ids (advertised_dead like gpt-image-1)
// never appear, even when priced.
func TestServableClientFacingIDs_InvariantAndAdvertisedDead(t *testing.T) {
	ctx := context.Background()
	allow := supportedCatalogModelIDsForPlatform(PlatformOpenAI)
	require.NotEmpty(t, allow, "openai allowlist must be populated")
	allowSet := boolSetForTest(allow)
	// Boundary samples: priced OpenAI media rows that must remain hidden from
	// the chat/model-list allowlist unless the SSOT owner promotes them.
	dead := []string{"gpt-image-1", "gpt-image-1.5", "gpt-image-2"}
	for _, d := range dead {
		require.False(t, allowSet[d], "precondition: %s must be advertised_dead (priced but NOT in allowlist)", d)
	}
	anyAllowID := firstStringForTest(t, allow)
	require.True(t, allowSet[anyAllowID], "precondition: sampled OpenAI SSOT id must be allowlisted")
	require.False(t, allowSet["codex-auto-review"], "codex-auto-review is an internal capability, never client-selectable (deprecated-model gate)")
	require.False(t, allowSet["gpt-5-pro"], "SSOT delta gate 403 model must not remain allowlisted")
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
	require.False(t, gotSet["codex-auto-review"], "codex-auto-review is an internal capability, never client-selectable (deprecated-model gate)")
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
	survivor := firstStringForTest(t, priced)
	require.True(t, gotSet[survivor], "a priced allowlist id (%s) must remain", survivor)
}

// TestServableClientFacingIDs_PrunesStructurallyGone confirms the unified source
// also drops structurally-gone ids (model_not_found→unreachable), so the
// /v1/models fallback self-heals identically to /pricing and Your-Menu.
func TestServableClientFacingIDs_PrunesStructurallyGone(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newAvailabilityTestService(t)
	ownerIDs := supportedCatalogModelIDsForPlatform(PlatformAnthropic)
	target, survivor := firstTwoStringsForTest(t, ownerIDs)
	pricing := tkBuildPricedServiceForTest(t, ownerIDs)
	baseline := ServableClientFacingIDs(ctx, PlatformAnthropic, svc, pricing)
	require.Contains(t, baseline, target, "SSOT-derived prune target must exist before availability changes")
	require.Contains(t, baseline, survivor, "SSOT-derived survivor must exist before availability changes")

	seedAvail(repo, PlatformAnthropic, target, AvailabilityStatusUnreachable, FailureKindModelNotFound)
	got := ServableClientFacingIDs(ctx, PlatformAnthropic, svc, pricing)
	require.NotContains(t, got, target, "structurally-gone model must be pruned from the unified servable source")
	require.Contains(t, got, survivor, "an unaffected SSOT sibling must remain servable")
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

	t.Run("anthropic draws from the servable allowlist (fable-5 prep restored)", func(t *testing.T) {
		svc, _, _ := newAvailabilityTestService(t)
		ids := tkServableCandidateIDs(ctx, PlatformAnthropic, svc)
		require.ElementsMatch(t, supportedCatalogModelIDsForPlatform(PlatformAnthropic), ids,
			"anthropic candidates must mirror the servable SSOT")
	})

	t.Run("structurally-gone model is pruned; degraded model stays", func(t *testing.T) {
		svc, repo, _ := newAvailabilityTestService(t)
		ownerIDs := supportedCatalogModelIDsForPlatform(PlatformAnthropic)
		gone, degraded := firstTwoStringsForTest(t, ownerIDs)
		baseline := tkServableCandidateIDs(ctx, PlatformAnthropic, svc)
		require.Contains(t, baseline, gone, "SSOT-derived prune target must exist before availability changes")
		require.Contains(t, baseline, degraded, "SSOT-derived degraded survivor must exist before availability changes")

		seedAvail(repo, PlatformAnthropic, gone, AvailabilityStatusUnreachable, FailureKindModelNotFound)
		seedAvail(repo, PlatformAnthropic, degraded, AvailabilityStatusUnreachable, FailureKindUpstream5xx)
		ids := tkServableCandidateIDs(ctx, PlatformAnthropic, svc)
		require.False(t, contains(ids, gone), "model_not_found→unreachable auto-drops (self-heal)")
		require.True(t, contains(ids, degraded), "transient 5xx-unreachable stays")
	})

	t.Run("antigravity draws from the empirically-servable gemini plus live Claude allowlist", func(t *testing.T) {
		svc, _, _ := newAvailabilityTestService(t)
		ids := tkServableCandidateIDs(ctx, PlatformAntigravity, svc)
		require.ElementsMatch(t, supportedCatalogModelIDsForPlatform(PlatformAntigravity), ids,
			"antigravity candidates must mirror the servable SSOT")
		gotSet := boolSetForTest(ids)
		for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini} {
			offPlatform := firstIDOutsideSetForTest(t, supportedCatalogModelIDsForPlatform(platform), gotSet)
			require.False(t, gotSet[offPlatform],
				"%s must not leak into antigravity client/admin defaults without a 200 allowlist entry", offPlatform)
		}
		require.False(t, contains(ids, "gpt-oss-120b-medium"),
			"unsupported gpt-oss boundary sample must not leak into antigravity defaults")
	})

	t.Run("nil availability degrades to no pruning (passthrough)", func(t *testing.T) {
		ids := tkServableCandidateIDs(ctx, PlatformAnthropic, nil)
		allow := supportedCatalogModelIDsForPlatform(PlatformAnthropic)
		require.ElementsMatch(t, allow, ids, "without availability the full allowlist passes through")
	})

	t.Run("gemini draws from empirical allowlist, not raw advertised defaults", func(t *testing.T) {
		svc, _, _ := newAvailabilityTestService(t)
		ids := tkServableCandidateIDs(ctx, PlatformGemini, svc)
		require.ElementsMatch(t, supportedCatalogModelIDsForPlatform(PlatformGemini), ids,
			"gemini candidates must mirror the empirical servable SSOT, not raw defaults")
	})
}

func boolSetForTest(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func firstStringForTest(t *testing.T, ids []string) string {
	t.Helper()
	require.NotEmpty(t, ids, "SSOT sample source must be populated")
	return ids[0]
}

func firstTwoStringsForTest(t *testing.T, ids []string) (string, string) {
	t.Helper()
	require.GreaterOrEqual(t, len(ids), 2, "SSOT sample source must contain a target and survivor")
	sorted := append([]string{}, ids...)
	sort.Strings(sorted)
	return sorted[0], sorted[1]
}

func firstIDOutsideSetForTest(t *testing.T, candidates []string, excluded map[string]bool) string {
	t.Helper()
	for _, id := range candidates {
		if !excluded[id] {
			return id
		}
	}
	require.FailNow(t, "expected at least one candidate outside excluded set")
	return ""
}

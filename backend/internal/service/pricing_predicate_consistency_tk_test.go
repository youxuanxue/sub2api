//go:build unit

package service

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// R3 predicate-consistency test (docs/approved/priced-or-it-doesnt-ship.md §7).
//
// The runtime priced-serving gate uses PricingCatalogService.tkIsModelEffectivelyPriced
// to decide pass/reject. The billing resolver uses BillingService.GetModelPricing.
// If these two drift, the gate is form-over-substance: it could pass a model that
// billing then charges $0 for (R3). This test pins the biconditional
//
//	tkIsModelEffectivelyPriced(m) ⟺ GetModelPricing(m) != ErrModelPricingUnavailable
//
// on a candidate set fed from ONE shared JSON blob, with explicit coverage of the
// "catalog entry present but token price all-zero" boundary that bare catalog
// membership (IsModelPriced) gets WRONG.

// newSharedPredicateFixture feeds the same blob to the catalog and the billing
// pricing service so both predicates resolve from one source of truth.
func newSharedPredicateFixture(t *testing.T, blob []byte) (*PricingCatalogService, *BillingService) {
	t.Helper()

	catalog := NewPricingCatalogService(nil)
	catalog.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return blob, time.Now(), true
	})

	ps := &PricingService{}
	data, err := ps.parsePricingData(blob)
	require.NoError(t, err)
	ps.pricingData = data

	billing := NewBillingService(nil, ps)
	return catalog, billing
}

func TestR3_GatePredicateConsistentWithBilling(t *testing.T) {
	// Synthetic model ids prefixed "r3-" so they cannot collide with the
	// compile-embedded overlay that BuildPublicCatalog unions in.
	blob := []byte(`{
		"r3-token-priced":   {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015, "litellm_provider": "test"},
		"r3-input-only":     {"input_cost_per_token": 0.000003, "output_cost_per_token": 0, "litellm_provider": "test"},
		"r3-zero-token":     {"input_cost_per_token": 0, "output_cost_per_token": 0, "litellm_provider": "test"},
		"r3-video-priced":   {"input_cost_per_token": 0, "output_cost_per_token": 0, "output_cost_per_second": 0.4, "mode": "video", "litellm_provider": "test"},
		"r3-image-priced":   {"input_cost_per_token": 0, "output_cost_per_token": 0, "output_cost_per_image": 0.04, "mode": "image", "litellm_provider": "test"}
	}`)

	catalog, billing := newSharedPredicateFixture(t, blob)

	// Candidate set = every key in the blob, PLUS a model absent from it.
	candidates := []string{
		"r3-token-priced",
		"r3-input-only",
		"r3-zero-token",
		"r3-video-priced",
		"r3-image-priced",
		"r3-absent-from-catalog",
	}

	for _, m := range candidates {
		t.Run(m, func(t *testing.T) {
			gatePriced := catalog.tkIsModelEffectivelyPriced(m, "")
			_, err := billing.GetModelPricing(m)
			billingResolvable := !errors.Is(err, ErrModelPricingUnavailable)

			require.Equal(t, billingResolvable, gatePriced,
				"R3 drift for %q: gate.tkIsModelEffectivelyPriced=%v but billing.resolvable=%v "+
					"— the gate must agree with the billing resolver or it is form-over-substance",
				m, gatePriced, billingResolvable)
		})
	}
}

// TestR3_ZeroTokenBoundary_BareMembershipDisagrees documents WHY the gate uses
// the stricter predicate: a present-but-zero token entry is a catalog MEMBER
// (IsModelPriced=true) yet billing-unavailable. This is the exact hole the gate
// would have if it used bare IsModelPriced, and the regression this test guards.
func TestR3_ZeroTokenBoundary_BareMembershipDisagrees(t *testing.T) {
	blob := []byte(`{
		"r3-zero-token": {"input_cost_per_token": 0, "output_cost_per_token": 0, "litellm_provider": "test"}
	}`)
	catalog, billing := newSharedPredicateFixture(t, blob)

	// Bare membership says "priced" (it's in the catalog) ...
	require.True(t, catalog.IsModelPriced("r3-zero-token", ""),
		"bare membership: a present-but-zero entry IS a catalog member")
	// ... but the gate predicate and billing both say "not priced".
	require.False(t, catalog.tkIsModelEffectivelyPriced("r3-zero-token", ""),
		"gate predicate must reject a present-but-zero entry")
	_, err := billing.GetModelPricing("r3-zero-token")
	require.ErrorIs(t, err, ErrModelPricingUnavailable,
		"billing resolves a present-but-zero entry to unavailable ($0 risk)")
}

// TestR3_MediaPricedModelsPass guards that legitimately media-priced models
// (zero token price, non-zero per-second/per-image) are NOT rejected by the
// gate — they are priced, just on a non-token unit.
func TestR3_MediaPricedModelsPass(t *testing.T) {
	blob := []byte(`{
		"r3-veo":      {"input_cost_per_token": 0, "output_cost_per_token": 0, "output_cost_per_second": 0.4, "litellm_provider": "test"},
		"r3-seedream": {"input_cost_per_token": 0, "output_cost_per_token": 0, "output_cost_per_image": 0.04, "litellm_provider": "test"}
	}`)
	catalog := NewPricingCatalogService(nil)
	catalog.SetSourceForTesting(func() ([]byte, time.Time, bool) { return blob, time.Now(), true })

	require.True(t, catalog.tkIsModelEffectivelyPriced("r3-veo", ""),
		"per-second video price is a real price")
	require.True(t, catalog.tkIsModelEffectivelyPriced("r3-seedream", ""),
		"per-image price is a real price")
}

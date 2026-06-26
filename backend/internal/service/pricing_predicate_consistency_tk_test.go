//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// R3 gate ⟺ billing consistency (docs/approved/priced-or-it-doesnt-ship.md §7).
//
// Root-cause refactor: the gate now decides pass/reject through the SAME oracle
// billing uses — BillingService.GetModelPricing — instead of a catalog shadow
// predicate (tkIsModelEffectivelyPriced). So "gate ⟺ billing" is no longer a
// property to police across two independent predicates; it is CONSTRUCTIVE: the
// gate calls GetModelPricing, billing calls GetModelPricing, same call same key.
//
// What these tests therefore pin is the part that is NOT free:
//   1. catch-all KEY consistency (BLOCKER1): when requested ≠ mapped and the
//      REQUESTED id is unpriced while the MAPPED id is priced, the gate must
//      reject on the key billing will actually charge (the requested/original
//      key on native gemini/anthropic), not be fooled by the priced mapped id.
//   2. boundary inputs through the REAL GetModelPricing: present-but-zero token
//      entry → unavailable → reject; media-priced (per-second/per-image) → priced
//      → pass; family-fallback id absent from the source → priced → pass.

// newConsistencyBilling builds a real BillingService over an in-test pricing blob
// (same shape as the catalog source). Family fallbacks (getFallbackPricing) apply
// on top, exactly as in production.
func newConsistencyBilling(t *testing.T, blob []byte) *BillingService {
	t.Helper()
	ps := &PricingService{}
	data, err := ps.parsePricingData(blob)
	require.NoError(t, err)
	ps.pricingData = data
	return NewBillingService(nil, ps)
}

// TestR3_CatchAllKeyConsistency_GateRejectsOnBillingKey is the BLOCKER1 fix
// pinned end to end at the decision level: a catch-all mapping {"*": priced}
// makes the MAPPED model priced, but billing charges the REQUESTED/original id.
// The gate, fed the requested key (as the native gemini/anthropic Forward paths
// now do), must REJECT — proving 闸键 == 账键 and the reverse leak is closed.
func TestR3_CatchAllKeyConsistency_GateRejectsOnBillingKey(t *testing.T) {
	ctx := context.Background()
	// Source prices the mapped target only; the requested client id is unpriced
	// (and not a fallback family).
	blob := []byte(`{
		"mapped-priced-target": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015, "litellm_provider": "test"}
	}`)
	billing := newConsistencyBilling(t, blob)
	resolve := billing.GetModelPricing
	setting := newGateSettingService("gemini")

	const requestedUnpriced = "client-garbage-not-priced"
	const mappedPriced = "mapped-priced-target"

	// Sanity: billing prices the mapped target, NOT the requested id.
	_, mappedErr := resolve(mappedPriced)
	require.False(t, errors.Is(mappedErr, ErrModelPricingUnavailable), "mapped target is priced")
	_, reqErr := resolve(requestedUnpriced)
	require.True(t, errors.Is(reqErr, ErrModelPricingUnavailable), "requested id is unpriced (the $0 billing key)")

	// Gate fed the MAPPED key (the OLD buggy behavior) would PASS — documenting
	// the leak the refactor closes.
	require.False(t, tkPricedServingGateRejected(ctx, resolve, setting, mappedPriced, "gemini"),
		"judging the mapped (priced) key PASSES — this was the leak")

	// Gate fed the REQUESTED/billing key (the FIXED behavior) must REJECT.
	require.True(t, tkPricedServingGateRejected(ctx, resolve, setting, requestedUnpriced, "gemini"),
		"BLOCKER1: gate must judge the exact key billing charges (requested/original) and reject the $0 leak")
}

// TestR3_BoundariesThroughRealBilling walks the boundary classes that bare
// catalog membership got wrong, now asserted through the SAME GetModelPricing the
// gate uses. gate-rejected ⟺ billing-unavailable is therefore tautological here,
// which is exactly the safety the refactor buys.
func TestR3_BoundariesThroughRealBilling(t *testing.T) {
	blob := []byte(`{
		"r3-token-priced":   {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015, "litellm_provider": "test"},
		"r3-input-only":     {"input_cost_per_token": 0.000003, "output_cost_per_token": 0, "litellm_provider": "test"},
		"r3-zero-token":     {"input_cost_per_token": 0, "output_cost_per_token": 0, "litellm_provider": "test"},
		"r3-video-priced":   {"input_cost_per_token": 0, "output_cost_per_token": 0, "output_cost_per_second": 0.4, "mode": "video", "litellm_provider": "test"},
		"r3-image-priced":   {"input_cost_per_token": 0, "output_cost_per_token": 0, "output_cost_per_image": 0.04, "mode": "image", "litellm_provider": "test"}
	}`)
	billing := newConsistencyBilling(t, blob)
	resolve := billing.GetModelPricing
	setting := newGateSettingService("gemini")
	ctx := context.Background()

	cases := []struct {
		model      string
		wantReject bool // i.e. billing-unavailable
		note       string
	}{
		{"r3-token-priced", false, "token-priced → pass"},
		{"r3-input-only", false, "input-only is a real price → pass"},
		{"r3-zero-token", true, "present-but-zero token → billing unavailable → reject"},
		{"r3-video-priced", false, "per-second media price → pass"},
		{"r3-image-priced", false, "per-image media price → pass"},
		{"r3-absent-not-family", true, "absent + no fallback family → reject"},
		{"gemini-new-variant-xyz", false, "absent but gemini family fallback → priced → pass (SHOULD-FIX1)"},
		{"claude-new-variant-xyz", false, "absent but claude family fallback → priced → pass (SHOULD-FIX1)"},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			_, err := resolve(tc.model)
			billingUnavailable := errors.Is(err, ErrModelPricingUnavailable)
			require.Equal(t, tc.wantReject, billingUnavailable, "billing availability: %s", tc.note)

			gotReject := tkPricedServingGateRejected(ctx, resolve, setting, tc.model, "gemini")
			require.Equal(t, tc.wantReject, gotReject,
				"gate reject must equal billing-unavailable (constructive consistency): %s", tc.note)
		})
	}
}

// TestR3_ImageTokenPriced_PriceThroughBilling pins SHOULD-FIX2 with a field the
// OLD catalog gate predicate (tkIsModelEffectivelyPriced) did NOT check: an entry
// priced ONLY via output_cost_per_image_token (zero per-token text price, no
// per-image / per-second). billing recognizes it (LiteLLMModelPricing carries
// OutputCostPerImageToken), so via GetModelPricing the gate inherits it for free;
// the old catalog projection (which only looked at OutputCostPerImage /
// OutputCostPerSecond) would have 404'd this priced model. The constructive
// gate==billing property generalizes the same way to priority/above-1hr/interval
// fields — none of them is re-implemented in the gate anymore.
func TestR3_ImageTokenPriced_PriceThroughBilling(t *testing.T) {
	blob := []byte(`{
		"r3-image-token-only": {"input_cost_per_token": 0, "output_cost_per_token": 0, "output_cost_per_image_token": 0.0001, "mode": "image", "litellm_provider": "test"}
	}`)
	billing := newConsistencyBilling(t, blob)
	resolve := billing.GetModelPricing
	setting := newGateSettingService("gemini")
	ctx := context.Background()

	// billing prices it via the image-token dimension (parsePricingData retains
	// the entry; GetModelPricing returns a non-unavailable result).
	_, err := resolve("r3-image-token-only")
	require.False(t, errors.Is(err, ErrModelPricingUnavailable),
		"billing prices an image-token-only entry (not unavailable)")

	require.False(t, tkPricedServingGateRejected(ctx, resolve, setting, "r3-image-token-only", "gemini"),
		"SHOULD-FIX2: a model billing prices via output_cost_per_image_token must NOT be gate-rejected "+
			"(the old catalog predicate, lacking this field, would have 404'd it)")
}

// (compile sanity for config import used by newGateSettingService elsewhere; the
// helper lives in gateway_priced_serving_gate_tk_test.go in the same package.)
var _ = config.Config{}
var _ = json.Marshal

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
		"mapped-priced-target": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015, "litellm_provider": "test"},
		"gemini-2.5-pro": {"input_cost_per_token": 0.00000125, "output_cost_per_token": 0.00001, "litellm_provider": "test"}
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
	require.False(t, tkPricedServingGateRejected(ctx, resolve, nil, setting, mappedPriced, "gemini", 0),
		"judging the mapped (priced) key PASSES — this was the leak")

	// Gate fed the REQUESTED/billing key (the FIXED behavior) must REJECT.
	require.True(t, tkPricedServingGateRejected(ctx, resolve, nil, setting, requestedUnpriced, "gemini", 0),
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
		"r3-image-priced":   {"input_cost_per_token": 0, "output_cost_per_token": 0, "output_cost_per_image": 0.04, "mode": "image", "litellm_provider": "test"},
		"gemini-2.5-pro":    {"input_cost_per_token": 0.00000125, "output_cost_per_token": 0.00001, "litellm_provider": "test"}
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
		{"gemini-new-variant-xyz", true, "gemini flat fallback REMOVED → unavailable → reject ('查不到就拒')"},
		{"claude-new-variant-xyz", false, "claude family fallback still applies → priced → pass"},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			_, err := resolve(tc.model)
			billingUnavailable := errors.Is(err, ErrModelPricingUnavailable)
			require.Equal(t, tc.wantReject, billingUnavailable, "billing availability: %s", tc.note)

			gotReject := tkPricedServingGateRejected(ctx, resolve, nil, setting, tc.model, "gemini", 0)
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

	require.False(t, tkPricedServingGateRejected(ctx, resolve, nil, setting, "r3-image-token-only", "gemini", 0),
		"SHOULD-FIX2: a model billing prices via output_cost_per_image_token must NOT be gate-rejected "+
			"(the old catalog predicate, lacking this field, would have 404'd it)")
}

// TestR3_ChannelPricedModelNotFalseRejected pins the re-review BLOCKER B1 fix: billing
// charges via TWO sources — GetModelPricing (litellm/overlay/fallback base) AND, when the
// group has channel_model_pricing, resolveChannelPricing (resolver.Resolve.Source==Channel).
// A model priced ONLY via the channel source has NO base price, so the old gate (which asked
// only GetModelPricing) would 404 a model billing happily charges — a false reject on the
// default-shipped gemini platform. The gate now probes the channel source too, restoring
// "gate ⟺ billing" on BOTH sources. This test feeds a stub channelProbe (standing in for
// resolver.Resolve.Source==PricingSourceChannel) and asserts the matrix.
func TestR3_ChannelPricedModelNotFalseRejected(t *testing.T) {
	ctx := context.Background()
	// Base pricing knows ONLY the canary (so the degraded probe reads healthy); the
	// channel-only model has NO base entry → GetModelPricing returns unavailable.
	blob := []byte(`{"gemini-2.5-pro": {"input_cost_per_token": 0.00000125, "output_cost_per_token": 0.00001, "litellm_provider": "test"}}`)
	resolve := newConsistencyBilling(t, blob).GetModelPricing
	setting := newGateSettingService("gemini")

	const channelOnly = "channel-only-priced-model"
	const groupWithChannel int64 = 42
	// Stub mirroring resolver.Resolve(...).Source==PricingSourceChannel: the group has a
	// channel price for channelOnly only.
	channelProbe := func(_ context.Context, model string, groupID int64) bool {
		return model == channelOnly && groupID == groupWithChannel
	}

	// Sanity: channelOnly has no BASE price (would 404 under the old base-only gate).
	_, err := resolve(channelOnly)
	require.True(t, errors.Is(err, ErrModelPricingUnavailable), "channel-only model has no base price")

	// B1 FIX: base missing + channel priced + real group → billing charges → gate PASSES.
	require.False(t,
		tkPricedServingGateRejected(ctx, resolve, channelProbe, setting, channelOnly, "gemini", groupWithChannel),
		"BLOCKER B1: a model priced via channel_model_pricing must NOT be 404'd (billing charges it)")

	// Control 1: nil channelProbe (old behavior / unwired resolver) → base-only → REJECT.
	require.True(t,
		tkPricedServingGateRejected(ctx, resolve, nil, setting, channelOnly, "gemini", groupWithChannel),
		"without the channel probe the model is (incorrectly) rejected — documents the pre-fix leak")

	// Control 2: groupID 0 (no group in context) → probe skipped → REJECT (safe degenerate).
	require.True(t,
		tkPricedServingGateRejected(ctx, resolve, channelProbe, setting, channelOnly, "gemini", 0),
		"groupID 0 skips the channel probe")

	// Control 3: a different unpriced model the group has NO channel price for → REJECT.
	require.True(t,
		tkPricedServingGateRejected(ctx, resolve, channelProbe, setting, "other-unpriced", "gemini", groupWithChannel),
		"channel probe returns false for a model without channel pricing → still rejected")
}

// TestTkResolvedPricingChargeable pins the adversarial-review fix to the B1 channel probe:
// a channel_model_pricing ROW existing (Source==Channel) is NOT enough to pass the gate —
// the row must be ACTUALLY chargeable. An all-empty row resolves to a $0 BasePricing that
// billing serves at $0 (served_zero_cost), so it must read as NOT chargeable → gate rejects,
// matching the base path where an all-zero entry is unpriced. Only a positive price in some
// dimension makes it chargeable.
func TestTkResolvedPricingChargeable(t *testing.T) {
	cases := []struct {
		name string
		r    *ResolvedPricing
		want bool
	}{
		{"nil", nil, false},
		{"all-empty channel row (the leak)", &ResolvedPricing{Source: PricingSourceChannel, BasePricing: &ModelPricing{}}, false},
		{"nil BasePricing", &ResolvedPricing{Source: PricingSourceChannel}, false},
		{"positive input token price", &ResolvedPricing{BasePricing: &ModelPricing{InputPricePerToken: 0.000003}}, true},
		{"positive output token price", &ResolvedPricing{BasePricing: &ModelPricing{OutputPricePerToken: 0.000015}}, true},
		{"positive cache-read only", &ResolvedPricing{BasePricing: &ModelPricing{CacheReadPricePerToken: 0.0000003}}, true},
		{"default per-request price", &ResolvedPricing{DefaultPerRequestPrice: 0.04, BasePricing: &ModelPricing{}}, true},
		{"per-request tier", &ResolvedPricing{RequestTiers: []PricingInterval{{PerRequestPrice: testPtrFloat64(0.04)}}, BasePricing: &ModelPricing{}}, true},
		{"per-request tier nil price", &ResolvedPricing{RequestTiers: []PricingInterval{{PerRequestPrice: nil}}, BasePricing: &ModelPricing{}}, false},
		{"token interval with price", &ResolvedPricing{Intervals: []PricingInterval{{InputPrice: testPtrFloat64(0.000003)}}, BasePricing: &ModelPricing{}}, true},
		{"token interval all-nil", &ResolvedPricing{Intervals: []PricingInterval{{}}, BasePricing: &ModelPricing{}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tkResolvedPricingChargeable(tc.r))
		})
	}
}

// (compile sanity for config import used by newGateSettingService elsewhere; the
// helper lives in gateway_priced_serving_gate_tk_test.go in the same package.)
var _ = config.Config{}
var _ = json.Marshal

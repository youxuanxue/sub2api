//go:build unit

package baseline

import "testing"

func TestLoadTierBaselineDoc(t *testing.T) {
	doc, err := LoadTierBaselineDoc()
	if err != nil {
		t.Fatalf("LoadTierBaselineDoc: %v", err)
	}
	if got := len(doc.Tiers); got != 5 {
		t.Fatalf("expected 5 tiers, got %d", got)
	}
	if doc.SharedBaseline.TLSProfile.Name != "tk_canonical_cc_oauth" {
		t.Fatalf("unexpected canonical tls profile name %q", doc.SharedBaseline.TLSProfile.Name)
	}
	wantOrder := []string{"l1", "l2", "l3", "l4", "l5"}
	if len(doc.Policy.TierOrder) != len(wantOrder) {
		t.Fatalf("tier_order = %v", doc.Policy.TierOrder)
	}
	for i, v := range wantOrder {
		if doc.Policy.TierOrder[i] != v {
			t.Fatalf("tier_order[%d] = %q want %q", i, doc.Policy.TierOrder[i], v)
		}
	}
}

// TestEffectiveBaselineForTier verifies the MERGE MECHANISM (shared_baseline +
// per-tier overlay), not specific numeric values. Effective account fields and
// tier-only extra keys are compared against the loaded doc itself, so legitimate
// baseline value changes (e.g. an across-the-board RPM bump) never require a test
// edit — the JSON stays the single source of truth.
func TestEffectiveBaselineForTier(t *testing.T) {
	doc, err := LoadTierBaselineDoc()
	if err != nil {
		t.Fatalf("LoadTierBaselineDoc: %v", err)
	}
	eff, err := EffectiveBaselineForTier("L4") // case-insensitive
	if err != nil {
		t.Fatalf("EffectiveBaselineForTier: %v", err)
	}
	tier := doc.Tiers["l4"]
	// account fields wired from tiers[l4].baseline.account (self-consistent, no magic numbers)
	if eff.Concurrency != tier.Baseline.Account.Concurrency || eff.Priority != tier.Baseline.Account.Priority {
		t.Fatalf("l4 concurrency/priority = %d/%d want %d/%d",
			eff.Concurrency, eff.Priority, tier.Baseline.Account.Concurrency, tier.Baseline.Account.Priority)
	}
	if eff.RateMultiplier != tier.Baseline.Account.RateMultiplier {
		t.Fatalf("l4 rate_multiplier = %v want %v", eff.RateMultiplier, tier.Baseline.Account.RateMultiplier)
	}
	// shared extra keys carried into the merge
	if v, _ := eff.Extra["rpm_strategy"].(string); v != "tiered" {
		t.Fatalf("expected shared extra rpm_strategy=tiered, got %v", eff.Extra["rpm_strategy"])
	}
	if v, _ := eff.Extra["enable_tls_fingerprint"].(bool); !v {
		t.Fatalf("expected enable_tls_fingerprint=true in merged extra")
	}
	// tier-only extra overlaid: value equals the per-tier source (mechanism, not a literal)
	if eff.Extra["base_rpm"] != tier.Baseline.Extra["base_rpm"] {
		t.Fatalf("l4 base_rpm = %v want %v (overlaid from tier)", eff.Extra["base_rpm"], tier.Baseline.Extra["base_rpm"])
	}
	if eff.Extra["max_sessions"] != tier.Baseline.Extra["max_sessions"] {
		t.Fatalf("l4 max_sessions = %v want %v (overlaid from tier)", eff.Extra["max_sessions"], tier.Baseline.Extra["max_sessions"])
	}
	// credentials carried from shared_baseline
	if _, ok := eff.Credentials["temp_unschedulable_rules"]; !ok {
		t.Fatalf("expected temp_unschedulable_rules in merged credentials")
	}
	// canonical TLS profile built with name + non-empty cipher suites
	prof := eff.CanonicalTLSProfile()
	if prof.Name != "tk_canonical_cc_oauth" {
		t.Fatalf("tls profile name = %q", prof.Name)
	}
	if len(prof.CipherSuites) == 0 {
		t.Fatalf("tls profile cipher_suites empty")
	}
}

// TestTierLadderInvariants asserts structural invariants that hold regardless of
// the exact numeric baselines, so value-only changes don't churn this test:
//   - every tier loads and carries the required positive extra caps
//   - shared extra keys are overlaid onto every tier
//   - tier priority is UNIFORM across tiers (caps-only tiers): stability tier
//     gates rate caps only, NOT scheduling order. Account scheduling order is
//     owned by the window-rebalance pipeline (accounts.priority); a uniform tier
//     base means tier no longer biases cross-tier scheduling. Asserting
//     uniformity (not a specific value) stays value-agnostic while catching an
//     accidental drift back to a per-tier priority ladder.
//
// NOTE: there is intentionally NO l1->l5 monotonic-non-decreasing assertion on
// the RPM / session / cost caps. Tiers are caps-only labels (priority uniform,
// scheduling owned by window-rebalance), so the tier NAME carries no ordering
// contract — operators repurpose individual slots freely (e.g. L1 is deliberately
// set to a high-capacity profile: L5 caps + concurrency 30). A monotonic guard
// would only fight that legitimate use; per-tier positive-caps below is the
// invariant that actually matters.
func TestTierLadderInvariants(t *testing.T) {
	order, err := TierOrder()
	if err != nil {
		t.Fatalf("TierOrder: %v", err)
	}
	requiredCaps := []string{"base_rpm", "rpm_sticky_buffer", "max_sessions"}
	var firstPriority int
	for i, name := range order {
		eff, err := EffectiveBaselineForTier(name)
		if err != nil {
			t.Fatalf("EffectiveBaselineForTier %q: %v", name, err)
		}
		if i == 0 {
			firstPriority = eff.Priority
		}
		if eff.Priority != firstPriority {
			t.Fatalf("%s priority = %d want %d (tier priority must be uniform — caps-only tiers; scheduling order is owned by window-rebalance)", name, eff.Priority, firstPriority)
		}
		if eff.Concurrency <= 0 {
			t.Fatalf("%s concurrency = %d want > 0", name, eff.Concurrency)
		}
		// shared overlay present on every tier
		if v, _ := eff.Extra["rpm_strategy"].(string); v != "tiered" {
			t.Fatalf("%s missing shared rpm_strategy=tiered", name)
		}
		for _, k := range requiredCaps {
			v, ok := eff.Extra[k].(float64)
			if !ok || v <= 0 {
				t.Fatalf("%s extra[%q] = %v want float64 > 0", name, k, eff.Extra[k])
			}
		}
	}
}

func TestEffectiveBaselineForTier_Unknown(t *testing.T) {
	if _, err := EffectiveBaselineForTier("l99"); err == nil {
		t.Fatalf("expected error for unknown tier")
	}
}

func TestLoadStubPoolBaseline(t *testing.T) {
	doc, re, err := LoadStubPoolBaseline()
	if err != nil {
		t.Fatalf("LoadStubPoolBaseline: %v", err)
	}
	if doc.Policy.PoolModeRetryCount != 3 {
		t.Fatalf("pool_mode_retry_count = %d want 3", doc.Policy.PoolModeRetryCount)
	}
	if !re.MatchString("https://api-us1.tokenkey.dev") {
		t.Fatalf("expected base_url regexp to match api-us1.tokenkey.dev")
	}
	if re.MatchString("https://api.openai.com") {
		t.Fatalf("base_url regexp should not match unrelated host")
	}
}

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

func TestEffectiveBaselineForTier(t *testing.T) {
	// l4 baseline from anthropic-oauth-stability-baselines-tiered.json:
	// account.concurrency=10 priority=4; extra.base_rpm=56 max_sessions=120 rpm_sticky_buffer=20
	eff, err := EffectiveBaselineForTier("L4") // case-insensitive
	if err != nil {
		t.Fatalf("EffectiveBaselineForTier: %v", err)
	}
	if eff.Concurrency != 10 || eff.Priority != 4 {
		t.Fatalf("l4 concurrency/priority = %d/%d want 10/4", eff.Concurrency, eff.Priority)
	}
	if eff.RateMultiplier != 1.0 {
		t.Fatalf("l4 rate_multiplier = %v want 1.0", eff.RateMultiplier)
	}
	// shared extra key present
	if v, _ := eff.Extra["rpm_strategy"].(string); v != "tiered" {
		t.Fatalf("expected shared extra rpm_strategy=tiered, got %v", eff.Extra["rpm_strategy"])
	}
	// shared enable_tls_fingerprint present
	if v, _ := eff.Extra["enable_tls_fingerprint"].(bool); !v {
		t.Fatalf("expected enable_tls_fingerprint=true in merged extra")
	}
	// tier-specific extra overrides/adds: base_rpm=56 (JSON number -> float64)
	if v, _ := eff.Extra["base_rpm"].(float64); v != 56 {
		t.Fatalf("l4 base_rpm = %v want 56", eff.Extra["base_rpm"])
	}
	if v, _ := eff.Extra["max_sessions"].(float64); v != 120 {
		t.Fatalf("l4 max_sessions = %v want 120", eff.Extra["max_sessions"])
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

func TestEffectiveBaselineForTier_LowerOverridesShared(t *testing.T) {
	// l1 sets cache_ttl_override_enabled=false, overriding shared true.
	eff, err := EffectiveBaselineForTier("l1")
	if err != nil {
		t.Fatalf("EffectiveBaselineForTier l1: %v", err)
	}
	if v, ok := eff.Extra["cache_ttl_override_enabled"].(bool); !ok || v {
		t.Fatalf("l1 cache_ttl_override_enabled = %v want false", eff.Extra["cache_ttl_override_enabled"])
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

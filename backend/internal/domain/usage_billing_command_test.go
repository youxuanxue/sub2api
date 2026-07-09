package domain

import "testing"

func TestUsageBillingCommandNormalize_TrimsRequestID(t *testing.T) {
	cmd := &UsageBillingCommand{RequestID: "  req-1  ", UserID: 1, AccountID: 2, APIKeyID: 3}
	cmd.Normalize()
	if cmd.RequestID != "req-1" {
		t.Fatalf("RequestID = %q, want req-1", cmd.RequestID)
	}
	if cmd.RequestFingerprint == "" {
		t.Fatal("expected fingerprint to be populated")
	}
}

func TestUsageBillingCommandNormalize_NilSafe(t *testing.T) {
	var cmd *UsageBillingCommand
	cmd.Normalize()
}

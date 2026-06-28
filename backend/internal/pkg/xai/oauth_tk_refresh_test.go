//go:build unit

package xai

import (
	"context"
	"strings"
	"testing"
)

// TestValidateEndpoint guards the SSRF boundary: the OAuth token endpoint must be
// https on an x.ai host. A poisoned discovery document pointing the refresh POST
// at an attacker host must be rejected.
func TestValidateEndpoint(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"https://auth.x.ai/oauth2/token", true},
		{"https://api.x.ai/v1", true},
		{"https://x.ai/token", true},
		{"http://auth.x.ai/oauth2/token", false},    // not https
		{"https://evil.com/token", false},           // wrong host
		{"https://auth.x.ai.evil.com/token", false}, // suffix-spoof
		{"", false},
		{"://broken", false},
	}
	for _, c := range cases {
		got, err := validateEndpoint(c.in)
		if c.ok && (err != nil || got == "") {
			t.Errorf("validateEndpoint(%q) = (%q, %v); want accepted", c.in, got, err)
		}
		if !c.ok && err == nil {
			t.Errorf("validateEndpoint(%q) = (%q, nil); want rejected", c.in, got)
		}
	}
}

// TestRefreshToken_InputGuards verifies the no-network guards: an empty
// refresh_token and a non-x.ai endpoint are rejected before any HTTP call.
func TestRefreshToken_InputGuards(t *testing.T) {
	if _, err := RefreshToken(context.Background(), "   ", "https://auth.x.ai/oauth2/token", ""); err == nil {
		t.Fatal("RefreshToken with empty refresh_token must error")
	}
	_, err := RefreshToken(context.Background(), "rt", "https://evil.example.com/token", "")
	if err == nil || !strings.Contains(err.Error(), "not under x.ai") {
		t.Fatalf("RefreshToken with non-x.ai endpoint must be rejected by the SSRF guard, got %v", err)
	}
}

// TestConstants locks the load-bearing public client identity (also anchored by
// scripts/sentinels/grok.json) so an accidental edit is caught at test time.
func TestConstants(t *testing.T) {
	if ClientID != "b1a00492-073a-47ea-816f-4c329264a828" {
		t.Errorf("ClientID drifted: %q", ClientID)
	}
	if DefaultAPIBaseURL != "https://api.x.ai/v1" {
		t.Errorf("DefaultAPIBaseURL drifted: %q", DefaultAPIBaseURL)
	}
}

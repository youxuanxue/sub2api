package service

import "testing"

func TestAntigravityTLS_DefaultEnabled(t *testing.T) {
	t.Parallel()
	a := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	if !a.IsTLSFingerprintEnabled() {
		t.Fatal("Antigravity OAuth should default TLS fingerprint ON")
	}
	if !a.isAntigravityOAuth() {
		t.Fatal("expected isAntigravityOAuth")
	}
}

func TestAntigravityTLS_ExplicitDisable(t *testing.T) {
	t.Parallel()
	a := &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"enable_tls_fingerprint": false},
	}
	if a.IsTLSFingerprintEnabled() {
		t.Fatal("explicit false should disable TLS fingerprint")
	}
}

func TestAntigravityTLS_NonAntigravityUnaffected(t *testing.T) {
	t.Parallel()
	a := &Account{Platform: PlatformGemini, Type: AccountTypeOAuth}
	if a.isAntigravityOAuth() {
		t.Fatal("gemini must not be treated as antigravity oauth")
	}
}

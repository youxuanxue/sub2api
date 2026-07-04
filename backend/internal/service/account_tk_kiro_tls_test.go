package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
)

func kiroTestAccount(extra map[string]any) *Account {
	return &Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Extra: extra}
}

// Kiro TLS fingerprint masking is default-on (Kiro egresses to AWS CodeWhisperer
// where a Go-default ClientHello stands out).
func TestKiroTLSFingerprintEnabledByDefault(t *testing.T) {
	if !kiroTestAccount(nil).IsTLSFingerprintEnabled() {
		t.Fatal("kiro account with nil extra must default-on TLS fingerprint")
	}
	if !kiroTestAccount(map[string]any{}).IsTLSFingerprintEnabled() {
		t.Fatal("kiro account with empty extra must default-on TLS fingerprint")
	}
}

func TestKiroTLSFingerprintOptOut(t *testing.T) {
	if kiroTestAccount(map[string]any{"enable_tls_fingerprint": false}).IsTLSFingerprintEnabled() {
		t.Fatal("kiro account with enable_tls_fingerprint=false must opt out")
	}
	if !kiroTestAccount(map[string]any{"enable_tls_fingerprint": true}).IsTLSFingerprintEnabled() {
		t.Fatal("kiro account with enable_tls_fingerprint=true must stay enabled")
	}
}

// Opening the gate for Kiro must not enable it for other platforms.
func TestNonKiroTLSGateUnchanged(t *testing.T) {
	apiKey := &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	if apiKey.IsTLSFingerprintEnabled() {
		t.Fatal("anthropic apikey account must stay disabled")
	}
	openai := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"enable_tls_fingerprint": true},
	}
	if openai.IsTLSFingerprintEnabled() {
		t.Fatal("openai account must stay disabled even with the extra flag set")
	}
}

func TestKiroMirrorStubModelSupportUsesKiroCatalog(t *testing.T) {
	stub := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": PlatformKiro,
		},
	}
	if !stub.IsKiroMirrorStub() {
		t.Fatal("fixture must be a Kiro mirror stub")
	}
	if !stub.IsModelSupported("claude-sonnet-4-5") {
		t.Fatal("Kiro mirror stub must support Kiro-served Claude ids")
	}
	if !stub.IsModelSupported("claude-opus-4-8") {
		t.Fatal("Kiro mirror stub must support Kiro-served Opus ids")
	}
	for _, denied := range []string{"claude-fable-5", "claude-opus-4-1"} {
		if stub.IsModelSupported(denied) {
			t.Fatalf("Kiro mirror stub must not claim unsupported model %q", denied)
		}
	}
}

func TestNativeKiroAccountModelSupportUsesKiroCatalog(t *testing.T) {
	kiro := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
	}
	if !kiro.IsModelSupported("claude-sonnet-4-6") {
		t.Fatal("native Kiro account must support Kiro-served Claude ids")
	}
	for _, denied := range []string{"claude-fable-5", "claude-opus-4-1"} {
		if kiro.IsModelSupported(denied) {
			t.Fatalf("native Kiro account must not claim unsupported model %q", denied)
		}
	}
}

func newTLSSvcWithProfiles(profiles ...*model.TLSFingerprintProfile) *TLSFingerprintProfileService {
	m := make(map[int64]*model.TLSFingerprintProfile, len(profiles))
	for i, p := range profiles {
		m[int64(i+1)] = p
	}
	return &TLSFingerprintProfileService{localCache: m}
}

// When the canonical Kiro profile is seeded, a default Kiro account resolves it by
// name (no explicit tls_fingerprint_profile_id binding needed).
func TestResolveTLSProfile_KiroByName(t *testing.T) {
	svc := newTLSSvcWithProfiles(&model.TLSFingerprintProfile{
		Name:         CanonicalKiroTLSProfileName,
		CipherSuites: []uint16{4865, 4866},
	})
	got := svc.ResolveTLSProfile(kiroTestAccount(nil))
	if got == nil || got.Name != CanonicalKiroTLSProfileName {
		t.Fatalf("kiro must resolve %q by name, got %+v", CanonicalKiroTLSProfileName, got)
	}
}

// Before the seed migration lands, a Kiro account must fall back to nil (plain TLS,
// safe) — NOT the Node.js 24.x built-in default, which is cc-shaped and a wrong
// fingerprint for Kiro.
func TestResolveTLSProfile_KiroMissingProfileFallsBackToNil(t *testing.T) {
	svc := newTLSSvcWithProfiles() // empty cache, profile not seeded yet
	if got := svc.ResolveTLSProfile(kiroTestAccount(nil)); got != nil {
		t.Fatalf("kiro without a seeded profile must resolve to nil, got %+v", got)
	}
}

// Regression: the anthropic OAuth path still falls back to the Node.js 24.x
// built-in default when enabled with no bound profile.
func TestResolveTLSProfile_AnthropicDefaultUnchanged(t *testing.T) {
	svc := newTLSSvcWithProfiles()
	a := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"enable_tls_fingerprint": true},
	}
	got := svc.ResolveTLSProfile(a)
	if got == nil || got.Name != "Built-in Default (Node.js 24.x)" {
		t.Fatalf("anthropic default fallback regressed, got %+v", got)
	}
}

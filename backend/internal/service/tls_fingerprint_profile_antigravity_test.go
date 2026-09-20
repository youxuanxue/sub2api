package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

func TestAntigravityClientProfileDefaultsToCLI(t *testing.T) {
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	if got := account.AntigravityClientProfile(); got != antigravity.ClientProfileCLI {
		t.Fatalf("default profile = %q, want %q", got, antigravity.ClientProfileCLI)
	}
}

func TestAntigravityClientProfileManagerIsOptIn(t *testing.T) {
	account := &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"antigravity_client_profile": "manager"},
	}
	if got := account.AntigravityClientProfile(); got != antigravity.ClientProfileManager {
		t.Fatalf("manager profile = %q, want %q", got, antigravity.ClientProfileManager)
	}
}

func TestResolveTLSProfile_AntigravityMissingProfileFallsBackToNil(t *testing.T) {
	t.Parallel()
	svc := newTLSSvcWithProfiles() // empty cache, profile not seeded yet
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	if got := svc.ResolveTLSProfile(account); got != nil {
		t.Fatalf("AG without seeded CLI profile must resolve to nil (not Node default), got %+v", got)
	}
}

func TestResolveTLSProfile_AntigravityByName(t *testing.T) {
	t.Parallel()
	svc := newTLSSvcWithProfiles(&model.TLSFingerprintProfile{
		Name:              CanonicalAntigravityCLITLSProfileName,
		ShuffleExtensions: true,
		CipherSuites:      []uint16{4865, 4866},
	})
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	got := svc.ResolveTLSProfile(account)
	if got == nil || got.Name != CanonicalAntigravityCLITLSProfileName {
		t.Fatalf("want %q, got %+v", CanonicalAntigravityCLITLSProfileName, got)
	}
}

func TestResolveTLSProfile_AntigravityManagerByName(t *testing.T) {
	t.Parallel()
	svc := newTLSSvcWithProfiles(&model.TLSFingerprintProfile{
		Name: CanonicalAntigravityManagerTLSProfileName,
	})
	account := &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"antigravity_client_profile": "manager"},
	}
	got := svc.ResolveTLSProfile(account)
	if got == nil || got.Name != CanonicalAntigravityManagerTLSProfileName {
		t.Fatalf("want %q, got %+v", CanonicalAntigravityManagerTLSProfileName, got)
	}
}

func TestResolveTLSProfile_AntigravityManagerMissingProfileFallsBackToNil(t *testing.T) {
	t.Parallel()
	svc := newTLSSvcWithProfiles()
	account := &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"antigravity_client_profile": "manager"},
	}
	if got := svc.ResolveTLSProfile(account); got != nil {
		t.Fatalf("manager profile without seed must resolve to nil, got %+v", got)
	}
}

func TestResolveTLSProfile_AntigravityManagerRejectsIncompatibleExplicitBinding(t *testing.T) {
	t.Parallel()
	svc := newTLSSvcWithProfiles(
		&model.TLSFingerprintProfile{Name: CanonicalAntigravityCLITLSProfileName},
		&model.TLSFingerprintProfile{Name: CanonicalAntigravityManagerTLSProfileName},
	)
	account := &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"antigravity_client_profile": "manager"},
	}
	account.Extra["tls_fingerprint_profile_id"] = int64(1)
	got := svc.ResolveTLSProfile(account)
	if got == nil || got.Name != CanonicalAntigravityManagerTLSProfileName {
		t.Fatalf("incompatible explicit profile should resolve Manager canonical, got %+v", got)
	}
}

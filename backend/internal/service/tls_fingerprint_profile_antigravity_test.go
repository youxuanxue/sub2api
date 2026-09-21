package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

func TestAntigravityClientProfileDefaultsToOfficialIDE(t *testing.T) {
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	if got := account.AntigravityClientProfile(); got != antigravity.ClientProfileIDE {
		t.Fatalf("default profile = %q, want %q", got, antigravity.ClientProfileIDE)
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

func TestResolveTLSProfile_AntigravityMissingProfileUsesOfficialIDEPreset(t *testing.T) {
	t.Parallel()
	svc := newTLSSvcWithProfiles() // empty cache, profile not seeded yet
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	if got := svc.ResolveTLSProfile(account); got == nil || got.Name != CanonicalAntigravityIDECloudcodeTLSProfileName {
		t.Fatalf("AG without seeded IDE profile must resolve to official IDE preset, got %+v", got)
	}
}

func TestResolveTLSProfile_AntigravityByName(t *testing.T) {
	t.Parallel()
	svc := newTLSSvcWithProfiles(&model.TLSFingerprintProfile{
		Name:              CanonicalAntigravityCLITLSProfileName,
		ShuffleExtensions: true,
		CipherSuites:      []uint16{4865, 4866},
	})
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Extra: map[string]any{"antigravity_client_profile": "cli"}}
	got := svc.ResolveTLSProfile(account)
	if got == nil || got.Name != CanonicalAntigravityCLITLSProfileName {
		t.Fatalf("want %q, got %+v", CanonicalAntigravityCLITLSProfileName, got)
	}
}

func TestResolveTLSProfile_AntigravityIDEByName(t *testing.T) {
	t.Parallel()
	svc := newTLSSvcWithProfiles(&model.TLSFingerprintProfile{
		Name:              CanonicalAntigravityIDECloudcodeTLSProfileName,
		ShuffleExtensions: false,
		Extensions:        []uint16{0, 11, 65281, 23, 18, 5, 10, 13, 50, 43, 51},
	})
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	got := svc.ResolveTLSProfile(account)
	if got == nil || got.Name != CanonicalAntigravityIDECloudcodeTLSProfileName {
		t.Fatalf("want %q, got %+v", CanonicalAntigravityIDECloudcodeTLSProfileName, got)
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

func TestResolveTLSProfile_AntigravityManagerMissingProfileUsesPreset(t *testing.T) {
	t.Parallel()
	svc := newTLSSvcWithProfiles()
	account := &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"antigravity_client_profile": "manager"},
	}
	if got := svc.ResolveTLSProfile(account); got == nil || got.Name != CanonicalAntigravityManagerTLSProfileName {
		t.Fatalf("manager profile without seed must resolve to the paired experimental preset, got %+v", got)
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

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
)

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

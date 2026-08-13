//go:build unit

package model

import "testing"

func TestTLSFingerprintProfileToTLSProfileCarriesExtensionShuffle(t *testing.T) {
	profile := (&TLSFingerprintProfile{
		Name:              "rustls_permuted",
		ShuffleExtensions: true,
		Extensions:        []uint16{0, 5, 10, 11, 13, 23, 35, 43, 45, 51},
	}).ToTLSProfile()

	if !profile.ShuffleExtensions {
		t.Fatal("expected shuffle_extensions to reach the runtime TLS profile")
	}
	if len(profile.Extensions) != 10 {
		t.Fatalf("expected extension set to be preserved, got %v", profile.Extensions)
	}
}

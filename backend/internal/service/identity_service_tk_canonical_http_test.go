package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fingerprintCacheRecorder struct {
	stored map[int64]*Fingerprint
}

func (r *fingerprintCacheRecorder) GetFingerprint(_ context.Context, accountID int64) (*Fingerprint, error) {
	if r.stored == nil {
		return nil, nil
	}
	return r.stored[accountID], nil
}

func (r *fingerprintCacheRecorder) SetFingerprint(_ context.Context, accountID int64, fp *Fingerprint) error {
	if r.stored == nil {
		r.stored = make(map[int64]*Fingerprint)
	}
	cp := *fp
	r.stored[accountID] = &cp
	return nil
}

func (r *fingerprintCacheRecorder) GetMaskedSessionID(context.Context, int64) (string, error) {
	return "", nil
}

func (r *fingerprintCacheRecorder) SetMaskedSessionID(context.Context, int64, string) error {
	return nil
}

func TestIsCanonicalTLSProfileName(t *testing.T) {
	require.True(t, IsCanonicalTLSProfileName("claude_cli_2_1_142_node24_20260515"))
	require.True(t, IsCanonicalTLSProfileName("  claude_cli_2_1_142_node24_20260515  "))
	require.False(t, IsCanonicalTLSProfileName("claude_cli_nodejs24_fixed"))
	require.False(t, IsCanonicalTLSProfileName(""))
}

func TestGetOrCreateFingerprint_CanonicalProfile_SeedsObservedHTTP(t *testing.T) {
	cache := &fingerprintCacheRecorder{}
	svc := NewIdentityService(cache)

	hdr := http.Header{}
	hdr.Set("User-Agent", "OpenAI/Python 2.38.0")

	fp, err := svc.GetOrCreateFingerprint(context.Background(), 1, hdr, canonicalTLSFingerprintProfileName)
	require.NoError(t, err)
	require.NotEmpty(t, fp.ClientID)
	require.Equal(t, canonicalHTTPObserved.UserAgent, fp.UserAgent)
	require.Equal(t, canonicalHTTPObserved.StainlessPackageVersion, fp.StainlessPackageVersion)
}

func TestGetOrCreateFingerprint_CanonicalProfile_DoesNotAdoptIngressUAUpgrade(t *testing.T) {
	cache := &fingerprintCacheRecorder{}
	svc := NewIdentityService(cache)

	seed := &Fingerprint{
		ClientID:                "client-seed",
		UserAgent:               canonicalHTTPObserved.UserAgent,
		StainlessLang:           canonicalHTTPObserved.StainlessLang,
		StainlessPackageVersion: canonicalHTTPObserved.StainlessPackageVersion,
		StainlessOS:             canonicalHTTPObserved.StainlessOS,
		StainlessArch:           canonicalHTTPObserved.StainlessArch,
		StainlessRuntime:        canonicalHTTPObserved.StainlessRuntime,
		StainlessRuntimeVersion: canonicalHTTPObserved.StainlessRuntimeVersion,
		UpdatedAt:               time.Now().Unix(),
	}
	require.NoError(t, cache.SetFingerprint(context.Background(), 2, seed))

	hdr := http.Header{}
	hdr.Set("User-Agent", "claude-cli/2.1.150 (external, cli)")
	hdr.Set("X-Stainless-Package-Version", "9.9.9")

	fp, err := svc.GetOrCreateFingerprint(context.Background(), 2, hdr, canonicalTLSFingerprintProfileName)
	require.NoError(t, err)
	require.Equal(t, canonicalHTTPObserved.UserAgent, fp.UserAgent)
	require.Equal(t, canonicalHTTPObserved.StainlessPackageVersion, fp.StainlessPackageVersion)
	require.Equal(t, "client-seed", fp.ClientID)
}

func TestGetOrCreateFingerprint_NonCanonical_StillMergesNewerIngressUA(t *testing.T) {
	cache := &fingerprintCacheRecorder{}
	svc := NewIdentityService(cache)

	seed := &Fingerprint{
		ClientID:  "client-old",
		UserAgent: "claude-cli/2.1.19 (external, cli)",
		UpdatedAt: time.Now().Unix(),
	}
	require.NoError(t, cache.SetFingerprint(context.Background(), 3, seed))

	hdr := http.Header{}
	hdr.Set("User-Agent", "claude-cli/2.1.150 (external, cli)")

	fp, err := svc.GetOrCreateFingerprint(context.Background(), 3, hdr, "")
	require.NoError(t, err)
	require.Equal(t, "claude-cli/2.1.150 (external, cli)", fp.UserAgent)
}

func TestApplyCanonicalHTTPObserved_RepairsStaleCache(t *testing.T) {
	fp := &Fingerprint{
		UserAgent:               "claude-cli/2.1.150 (external, cli)",
		StainlessPackageVersion: "0.70.0",
	}
	require.True(t, applyCanonicalHTTPObserved(fp))
	require.Equal(t, canonicalHTTPObserved.UserAgent, fp.UserAgent)
	require.Equal(t, canonicalHTTPObserved.StainlessPackageVersion, fp.StainlessPackageVersion)
}

//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
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

// resetResolver clears the package-level resolver so tests do not leak state.
func resetResolver(t *testing.T) {
	t.Helper()
	SetClaudeCodeUserAgentResolver(nil)
	t.Cleanup(func() { SetClaudeCodeUserAgentResolver(nil) })
}

func TestIsCanonicalTLSProfileName(t *testing.T) {
	require.True(t, IsCanonicalTLSProfileName("tk_canonical_cc_oauth"))
	require.True(t, IsCanonicalTLSProfileName("  tk_canonical_cc_oauth  "))
	require.False(t, IsCanonicalTLSProfileName("claude_cli_2_1_142_node24_20260515"))
	require.False(t, IsCanonicalTLSProfileName("claude_cli_nodejs24_fixed"))
	require.False(t, IsCanonicalTLSProfileName(""))
}

func TestNormalizeClaudeCodeUserAgentVersion(t *testing.T) {
	cases := map[string]string{
		"2.1.150":           "2.1.150",
		"  2.1.150  ":       "2.1.150",
		"2.1.150-rc.1":      "", // suffix not allowed
		"v2.1.150":          "", // v-prefix not allowed
		"2.1":               "", // 2-part semver not allowed
		"2.1.150.b91":       "", // 4-part not allowed
		"":                  "",
		"OpenAI/Python 1.0": "",
	}
	for in, want := range cases {
		require.Equalf(t, want, NormalizeClaudeCodeUserAgentVersion(in), "input=%q", in)
	}
}

func TestBuildCanonicalUserAgent_ValidVersion(t *testing.T) {
	ua := BuildCanonicalUserAgent("2.1.150")
	require.Equal(t, "claude-cli/2.1.150 (external, cli)", ua)
}

func TestBuildCanonicalUserAgent_InvalidFallsBackToDefault(t *testing.T) {
	for _, in := range []string{"", "garbage", "1.2", "v1.2.3"} {
		got := BuildCanonicalUserAgent(in)
		require.True(t, strings.HasPrefix(got, "claude-cli/"))
		require.True(t, strings.HasSuffix(got, " (external, cli)"))
		// 中间 version 段必须 = 当前 compile/env default
		require.Contains(t, got, GetDefaultClaudeCodeUserAgentVersion())
	}
}

func TestSetClaudeCodeUserAgentResolver_OverridesDefault(t *testing.T) {
	resetResolver(t)
	SetClaudeCodeUserAgentResolver(func(context.Context) string { return "9.9.9" })
	require.Equal(t, "9.9.9", GetClaudeCodeUserAgentVersionForContext(context.Background()))
	require.Equal(t, "claude-cli/9.9.9 (external, cli)",
		GetCanonicalUserAgentForContext(context.Background()))
}

func TestSetClaudeCodeUserAgentResolver_InvalidReturnFallsBackToDefault(t *testing.T) {
	resetResolver(t)
	SetClaudeCodeUserAgentResolver(func(context.Context) string { return "not-a-semver" })
	require.Equal(t, GetDefaultClaudeCodeUserAgentVersion(),
		GetClaudeCodeUserAgentVersionForContext(context.Background()))
}

func TestGetCanonicalUserAgentForContext_WithoutResolver_UsesDefault(t *testing.T) {
	resetResolver(t)
	ua := GetCanonicalUserAgentForContext(context.Background())
	expectedDefault := "claude-cli/" + GetDefaultClaudeCodeUserAgentVersion() + " (external, cli)"
	require.Equal(t, expectedDefault, ua)
}

func TestApplyCanonicalHTTPObserved_AdoptsExplicitUA(t *testing.T) {
	fp := &Fingerprint{
		UserAgent:               "claude-cli/2.0.1 (external, cli)",
		StainlessPackageVersion: "0.70.0",
	}
	require.True(t, applyCanonicalHTTPObserved(fp, "claude-cli/2.1.180 (external, cli)"))
	require.Equal(t, "claude-cli/2.1.180 (external, cli)", fp.UserAgent)
	require.Equal(t, canonicalHTTPObservedStatic.StainlessPackageVersion, fp.StainlessPackageVersion)
}

func TestApplyCanonicalHTTPObserved_EmptyUAFallsBackToDefault(t *testing.T) {
	fp := &Fingerprint{}
	require.True(t, applyCanonicalHTTPObserved(fp, ""))
	require.Contains(t, fp.UserAgent, GetDefaultClaudeCodeUserAgentVersion())
	require.Equal(t, canonicalHTTPObservedStatic.StainlessOS, fp.StainlessOS)
}

func TestApplyCanonicalHTTPObserved_NoOpWhenInSync(t *testing.T) {
	ua := BuildCanonicalUserAgent("2.1.150")
	fp := &Fingerprint{
		UserAgent:               ua,
		StainlessLang:           canonicalHTTPObservedStatic.StainlessLang,
		StainlessPackageVersion: canonicalHTTPObservedStatic.StainlessPackageVersion,
		StainlessOS:             canonicalHTTPObservedStatic.StainlessOS,
		StainlessArch:           canonicalHTTPObservedStatic.StainlessArch,
		StainlessRuntime:        canonicalHTTPObservedStatic.StainlessRuntime,
		StainlessRuntimeVersion: canonicalHTTPObservedStatic.StainlessRuntimeVersion,
	}
	require.False(t, applyCanonicalHTTPObserved(fp, ua),
		"already in sync should not flag changed=true")
}

func TestGetOrCreateFingerprint_CanonicalProfile_SeedsCanonicalHTTP(t *testing.T) {
	resetResolver(t)
	cache := &fingerprintCacheRecorder{}
	svc := NewIdentityService(cache)

	hdr := http.Header{}
	hdr.Set("User-Agent", "OpenAI/Python 2.38.0")

	fp, err := svc.GetOrCreateFingerprint(context.Background(), 1, hdr, canonicalTLSFingerprintProfileName)
	require.NoError(t, err)
	require.NotEmpty(t, fp.ClientID)
	expectedUA := GetCanonicalUserAgentForContext(context.Background())
	require.Equal(t, expectedUA, fp.UserAgent)
	require.Equal(t, canonicalHTTPObservedStatic.StainlessPackageVersion, fp.StainlessPackageVersion)
}

func TestGetOrCreateFingerprint_CanonicalProfile_DoesNotAdoptIngressUAUpgrade(t *testing.T) {
	resetResolver(t)
	cache := &fingerprintCacheRecorder{}
	svc := NewIdentityService(cache)

	canonicalUA := GetCanonicalUserAgentForContext(context.Background())
	seed := &Fingerprint{
		ClientID:                "client-seed",
		UserAgent:               canonicalUA,
		StainlessLang:           canonicalHTTPObservedStatic.StainlessLang,
		StainlessPackageVersion: canonicalHTTPObservedStatic.StainlessPackageVersion,
		StainlessOS:             canonicalHTTPObservedStatic.StainlessOS,
		StainlessArch:           canonicalHTTPObservedStatic.StainlessArch,
		StainlessRuntime:        canonicalHTTPObservedStatic.StainlessRuntime,
		StainlessRuntimeVersion: canonicalHTTPObservedStatic.StainlessRuntimeVersion,
		UpdatedAt:               time.Now().Unix(),
	}
	require.NoError(t, cache.SetFingerprint(context.Background(), 2, seed))

	hdr := http.Header{}
	hdr.Set("User-Agent", "claude-cli/9.9.9 (external, sdk-cli)")
	hdr.Set("X-Stainless-Package-Version", "9.9.9")

	fp, err := svc.GetOrCreateFingerprint(context.Background(), 2, hdr, canonicalTLSFingerprintProfileName)
	require.NoError(t, err)
	require.Equal(t, canonicalUA, fp.UserAgent)
	require.Equal(t, canonicalHTTPObservedStatic.StainlessPackageVersion, fp.StainlessPackageVersion)
	require.Equal(t, "client-seed", fp.ClientID)
}

func TestGetOrCreateFingerprint_CanonicalProfile_NormalizesObservedIncidentUAs(t *testing.T) {
	resetResolver(t)
	cache := &fingerprintCacheRecorder{}
	svc := NewIdentityService(cache)

	observedUAs := []string{
		"claude-cli/2.1.187 (external, cli)",
		"claude-cli/2.1.197 (external, cli)",
		"claude-cli/2.1.205 (external, sdk-cli)",
		"claude-cli/2.1.195 (external, claude-vscode, agent-sdk/0.3.195)",
		"claude-cli/2.1.196 (external, claude-vscode, agent-sdk/0.3.196)",
		"claude-cli/2.1.198 (external, claude-vscode, agent-sdk/0.3.198)",
	}
	expectedUA := GetCanonicalUserAgentForContext(context.Background())
	var clientID string

	for _, ua := range observedUAs {
		t.Run(ua, func(t *testing.T) {
			hdr := http.Header{}
			hdr.Set("User-Agent", ua)
			hdr.Set("X-Stainless-Package-Version", "9.9.9")
			hdr.Set("X-Stainless-OS", "Linux")
			hdr.Set("X-Stainless-Runtime-Version", "v99.99.99")

			fp, err := svc.GetOrCreateFingerprint(context.Background(), 13, hdr, canonicalTLSFingerprintProfileName)
			require.NoError(t, err)
			require.Equal(t, expectedUA, fp.UserAgent)
			require.Equal(t, canonicalHTTPObservedStatic.StainlessPackageVersion, fp.StainlessPackageVersion)
			require.Equal(t, canonicalHTTPObservedStatic.StainlessOS, fp.StainlessOS)
			require.Equal(t, canonicalHTTPObservedStatic.StainlessRuntimeVersion, fp.StainlessRuntimeVersion)
			if clientID == "" {
				clientID = fp.ClientID
			}
			require.Equal(t, clientID, fp.ClientID)
		})
	}
}

func TestGetOrCreateFingerprint_CanonicalProfile_AdoptsResolverUpdate(t *testing.T) {
	resetResolver(t)
	cache := &fingerprintCacheRecorder{}
	svc := NewIdentityService(cache)

	// Seed with the current default canonical UA.
	defaultUA := GetCanonicalUserAgentForContext(context.Background())
	seed := &Fingerprint{
		ClientID:                "client-seed",
		UserAgent:               defaultUA,
		StainlessLang:           canonicalHTTPObservedStatic.StainlessLang,
		StainlessPackageVersion: canonicalHTTPObservedStatic.StainlessPackageVersion,
		StainlessOS:             canonicalHTTPObservedStatic.StainlessOS,
		StainlessArch:           canonicalHTTPObservedStatic.StainlessArch,
		StainlessRuntime:        canonicalHTTPObservedStatic.StainlessRuntime,
		StainlessRuntimeVersion: canonicalHTTPObservedStatic.StainlessRuntimeVersion,
		UpdatedAt:               time.Now().Unix(),
	}
	require.NoError(t, cache.SetFingerprint(context.Background(), 4, seed))

	// Operator bumps the runtime UA setting (simulating admin UI / API change).
	SetClaudeCodeUserAgentResolver(func(context.Context) string { return "2.1.180" })

	hdr := http.Header{}
	hdr.Set("User-Agent", "claude-cli/2.1.150 (external, cli)") // ingress still on old
	fp, err := svc.GetOrCreateFingerprint(context.Background(), 4, hdr, canonicalTLSFingerprintProfileName)
	require.NoError(t, err)
	require.Equal(t, "claude-cli/2.1.180 (external, cli)", fp.UserAgent,
		"canonical path must adopt the new resolver-driven UA on next request (self-heal, no Redis clear required)")
}

func TestGetOrCreateFingerprint_NonCanonical_StillMergesNewerIngressUA(t *testing.T) {
	resetResolver(t)
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

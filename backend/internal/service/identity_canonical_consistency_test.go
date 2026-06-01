//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

// TestCanonicalIdentityIsSingleSourceOfTruth mechanically locks the three
// Claude Code HTTP identity definitions to ONE truth:
//
//  1. canonicalHTTPObservedStatic + canonicalUASuffix (service) — AUTHORITATIVE,
//     captured together with the single canonical TLS ClientHello.
//  2. defaultFingerprint (service)          — createFingerprintFromHeaders fallback.
//  3. claude.DefaultHeaders (pkg/claude)    — applyClaudeCodeMimicHeaders / models fallback.
//
// Before this guard, #2 and #3 independently declared Linux / node v24.13.0 /
// "(external, cli)" while #1 declared MacOS / node v24.3.0 / "(external, sdk-cli)".
// A request whose TLS used the canonical Node-24 ClientHello but whose HTTP
// headers fell to #2/#3 emitted a self-contradicting fingerprint (Mac-class TLS
// + Linux headers) — more suspicious to Anthropic's cohort risk-control than no
// mimicry at all. pkg/claude cannot import service (layering: service → claude),
// so #3 must be a synced literal; this test is the lock that forbids drift.
func TestCanonicalIdentityIsSingleSourceOfTruth(t *testing.T) {
	// #2 defaultFingerprint must be a pure derivation of the canonical block.
	require.Equal(t, canonicalHTTPObservedStatic.StainlessLang, defaultFingerprint.StainlessLang, "defaultFingerprint.StainlessLang drifted from canonical")
	require.Equal(t, canonicalHTTPObservedStatic.StainlessPackageVersion, defaultFingerprint.StainlessPackageVersion, "defaultFingerprint.StainlessPackageVersion drifted from canonical")
	require.Equal(t, canonicalHTTPObservedStatic.StainlessOS, defaultFingerprint.StainlessOS, "defaultFingerprint.StainlessOS drifted from canonical")
	require.Equal(t, canonicalHTTPObservedStatic.StainlessArch, defaultFingerprint.StainlessArch, "defaultFingerprint.StainlessArch drifted from canonical")
	require.Equal(t, canonicalHTTPObservedStatic.StainlessRuntime, defaultFingerprint.StainlessRuntime, "defaultFingerprint.StainlessRuntime drifted from canonical")
	require.Equal(t, canonicalHTTPObservedStatic.StainlessRuntimeVersion, defaultFingerprint.StainlessRuntimeVersion, "defaultFingerprint.StainlessRuntimeVersion drifted from canonical")
	require.True(t, strings.HasPrefix(defaultFingerprint.UserAgent, canonicalUAPrefix), "defaultFingerprint.UserAgent must use canonical prefix")
	require.True(t, strings.HasSuffix(defaultFingerprint.UserAgent, canonicalUASuffix), "defaultFingerprint.UserAgent must use canonical suffix (got %q)", defaultFingerprint.UserAgent)

	// #3 claude.DefaultHeaders must carry byte-identical canonical identity
	// fields. These are the keys that, mismatched against the canonical TLS
	// ClientHello, produce the self-contradicting fingerprint.
	require.Equal(t, canonicalHTTPObservedStatic.StainlessLang, claude.DefaultHeaders["X-Stainless-Lang"], "claude.DefaultHeaders X-Stainless-Lang drifted from canonical")
	require.Equal(t, canonicalHTTPObservedStatic.StainlessPackageVersion, claude.DefaultHeaders["X-Stainless-Package-Version"], "claude.DefaultHeaders X-Stainless-Package-Version drifted from canonical")
	require.Equal(t, canonicalHTTPObservedStatic.StainlessOS, claude.DefaultHeaders["X-Stainless-OS"], "claude.DefaultHeaders X-Stainless-OS drifted from canonical (the Mac/Linux mismatch)")
	require.Equal(t, canonicalHTTPObservedStatic.StainlessArch, claude.DefaultHeaders["X-Stainless-Arch"], "claude.DefaultHeaders X-Stainless-Arch drifted from canonical")
	require.Equal(t, canonicalHTTPObservedStatic.StainlessRuntime, claude.DefaultHeaders["X-Stainless-Runtime"], "claude.DefaultHeaders X-Stainless-Runtime drifted from canonical")
	require.Equal(t, canonicalHTTPObservedStatic.StainlessRuntimeVersion, claude.DefaultHeaders["X-Stainless-Runtime-Version"], "claude.DefaultHeaders X-Stainless-Runtime-Version drifted from canonical")

	ua := claude.DefaultHeaders["User-Agent"]
	require.True(t, strings.HasPrefix(ua, canonicalUAPrefix), "claude.DefaultHeaders User-Agent must use canonical prefix (got %q)", ua)
	require.True(t, strings.HasSuffix(ua, canonicalUASuffix), "claude.DefaultHeaders User-Agent must use canonical suffix; the (external, cli) vs (external, sdk-cli) split is the bug this guards (got %q)", ua)
}

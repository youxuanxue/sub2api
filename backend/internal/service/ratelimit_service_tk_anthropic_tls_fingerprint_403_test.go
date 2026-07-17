//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkMatchAnthropicTLSFingerprint403Body(t *testing.T) {
	require.Equal(t, "ja3", tkMatchAnthropicTLSFingerprint403Body(
		"",
		[]byte(`{"error":{"message":"forbidden: JA3 fingerprint does not match"}}`),
	))
	require.Empty(t, tkMatchAnthropicTLSFingerprint403Body(
		"",
		[]byte(`{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."}}`),
	))
}

func TestTkAnthropicTLSFingerprintDisablePrefixDistinctFromOrgBan(t *testing.T) {
	require.NotContains(t, tkAnthropicTLSFingerprintDisablePrefix, "Organization OAuth ban")
	require.Contains(t, tkAnthropicTLSFingerprintDisablePrefix, "TLS fingerprint profile stale")
}

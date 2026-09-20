package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

// CanonicalAntigravityCLITLSProfileName is the seeded TLS template that matches
// the captured Antigravity CLI ClientHello (paired with the CLI User-Agent).
const CanonicalAntigravityCLITLSProfileName = "tk_canonical_antigravity_cli"

// CanonicalAntigravityManagerTLSProfileName is an opt-in experimental profile
// paired with Antigravity-Manager headers and its Chrome-compatible transport.
const CanonicalAntigravityManagerTLSProfileName = "tk_canonical_antigravity_manager_chrome123"

const antigravityClientProfileExtraKey = "antigravity_client_profile"

func (a *Account) isAntigravityOAuth() bool {
	if a == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a.Platform), PlatformAntigravity) &&
		strings.EqualFold(strings.TrimSpace(a.Type), AccountTypeOAuth)
}

// AntigravityClientProfile returns the account-scoped wire identity family.
// Existing accounts remain on the captured CLI route until an operator sets
// extra.antigravity_client_profile=manager for a controlled experiment.
func (a *Account) AntigravityClientProfile() string {
	if a == nil || !a.isAntigravityOAuth() || a.Extra == nil {
		return antigravity.ClientProfileCLI
	}
	if v, ok := a.Extra[antigravityClientProfileExtraKey].(string); ok && strings.EqualFold(strings.TrimSpace(v), antigravity.ClientProfileManager) {
		return antigravity.ClientProfileManager
	}
	return antigravity.ClientProfileCLI
}

// isAntigravityTLSFingerprintEnabled mirrors Kiro: default ON for Antigravity
// OAuth unless explicitly disabled via extra.enable_tls_fingerprint=false.
// Profile is resolved by name in ResolveTLSProfile; when the CLI template is
// not seeded yet, GetProfileByName returns nil → plain TLS (never Node.js default).
func (a *Account) isAntigravityTLSFingerprintEnabled() bool {
	if a == nil || !a.isAntigravityOAuth() {
		return false
	}
	if a.Extra != nil {
		if v, ok := a.Extra["enable_tls_fingerprint"]; ok {
			if enabled, ok := v.(bool); ok {
				return enabled
			}
		}
	}
	return true
}

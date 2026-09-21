package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// CanonicalAntigravityCLITLSProfileName is the seeded TLS template that matches
// the captured Antigravity CLI ClientHello (paired with the CLI User-Agent).
const CanonicalAntigravityCLITLSProfileName = "tk_canonical_antigravity_cli"

// CanonicalAntigravityIDECloudcodeTLSProfileName is the official LS cloudcode
// ClientHello captured from a local non-forwarding CONNECT sink.
const CanonicalAntigravityIDECloudcodeTLSProfileName = tlsfingerprint.AntigravityIDECloudcodeProfileName

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
// Official IDE/LS is the default. CLI and Manager remain explicit opt-in
// compatibility routes through extra.antigravity_client_profile.
func (a *Account) AntigravityClientProfile() string {
	if a == nil || !a.isAntigravityOAuth() {
		return antigravity.ClientProfileCLI
	}
	if a.Extra == nil {
		return antigravity.ClientProfileIDE
	}
	if v, ok := a.Extra[antigravityClientProfileExtraKey].(string); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case antigravity.ClientProfileManager:
			return antigravity.ClientProfileManager
		case antigravity.ClientProfileCLI:
			return antigravity.ClientProfileCLI
		case antigravity.ClientProfileIDE:
			return antigravity.ClientProfileIDE
		}
	}
	return antigravity.ClientProfileIDE
}

// isAntigravityTLSFingerprintEnabled mirrors Kiro: default ON for Antigravity
// OAuth unless explicitly disabled via extra.enable_tls_fingerprint=false.
// Profile is resolved by identity name in ResolveTLSProfile; missing canonical
// data uses the selected built-in preset rather than the Node.js default.
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

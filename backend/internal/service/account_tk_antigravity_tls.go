package service

import "strings"

// CanonicalAntigravityCLITLSProfileName is the seeded TLS template that matches
// Antigravity CLI JA3/HTTP2 (paired with the CLI User-Agent). Do not fall back
// to Chrome123 / Node.js defaults for Antigravity OAuth egress.
const CanonicalAntigravityCLITLSProfileName = "tk_canonical_antigravity_cli"

func (a *Account) isAntigravityOAuth() bool {
	if a == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a.Platform), PlatformAntigravity) &&
		strings.EqualFold(strings.TrimSpace(a.Type), AccountTypeOAuth)
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

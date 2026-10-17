package service

import "time"

// grokDefaultExpiresIn is the conservative access-token lifetime (seconds)
// assumed when xAI omits expires_in, so NeedsRefresh still fires on a sane
// cadence. Used by the stdlib-only pkg/xai refresh path
// (admin_service_tk_grok_save.go).
const grokDefaultExpiresIn = 3600

func (a *Account) IsGrokAPIKey() bool {
	return a.IsGrok() && a.Type == AccountTypeAPIKey
}

// GetGrokTokenEndpoint returns the cached OIDC token_endpoint (optional; empty
// triggers discovery on the next refresh).
func (a *Account) GetGrokTokenEndpoint() string { return a.GetCredential("token_endpoint") }

// GetGrokExpiresAt returns the access-token expiry, or nil if unset/unparseable.
func (a *Account) GetGrokExpiresAt() *time.Time { return a.GetCredentialAsTime("expires_at") }

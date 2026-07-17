package service

import "time"

// grokDefaultExpiresIn is the conservative access-token lifetime (seconds)
// assumed when xAI omits expires_in, so NeedsRefresh still fires on a sane
// cadence. Used by the stdlib-only pkg/xai refresh path
// (admin_service_tk_grok_save.go).
const grokDefaultExpiresIn = 3600

// TokenKey-unique Grok (seventh platform) account helpers. The base grok
// predicates and credential getters (IsGrok / IsGrokOAuth / GetGrokBaseURL /
// GetGrokAccessToken / GetGrokRefreshToken) live in account.go; this companion
// only adds the helpers TokenKey needs on top of them (edge-relay API-key
// detection + the refresh-token endpoint/expiry getters used by the
// stdlib-only pkg/xai refresh path in admin_service_tk_grok_save.go).

// IsGrokAPIKey reports a grok edge-relay stub account (platform=grok, type=apikey).
func (a *Account) IsGrokAPIKey() bool {
	return a.IsGrok() && a.Type == AccountTypeAPIKey
}

// GetGrokTokenEndpoint returns the cached OIDC token_endpoint (optional; empty
// triggers discovery on the next refresh).
func (a *Account) GetGrokTokenEndpoint() string { return a.GetCredential("token_endpoint") }

// GetGrokExpiresAt returns the access-token expiry, or nil if unset/unparseable.
func (a *Account) GetGrokExpiresAt() *time.Time { return a.GetCredentialAsTime("expires_at") }

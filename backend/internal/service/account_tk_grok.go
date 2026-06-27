package service

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// IsGrok reports whether the account is a Grok (seventh platform) account.
//
// Unlike Kiro, xAI / Grok speaks the OpenAI-compatible wire protocol, so a grok
// account is an OpenAI-compat pool member (see engine.OpenAICompatPlatforms) and
// reuses the OpenAI-compat routing/scheduling/forward path. It differs from the
// openai (Codex) platform only in its OAuth refresh endpoint (auth.x.ai) and its
// inference base URL (api.x.ai/v1) — and crucially it forwards like the APIKey
// branch (plain Bearer + base_url), NOT the ChatGPT/Codex OAuth branch.
func (a *Account) IsGrok() bool {
	return a.Platform == PlatformGrok
}

func (a *Account) IsGrokOAuth() bool {
	return a.IsGrok() && a.Type == AccountTypeOAuth
}

// IsGrokAPIKey reports whether the account is a Grok account using API key credentials.
func (a *Account) IsGrokAPIKey() bool {
	return a.IsGrok() && a.Type == AccountTypeAPIKey
}

// GetGrokBaseURL returns the xAI inference base URL for this account, honoring a
// per-account credentials.base_url override and defaulting to api.x.ai/v1.
func (a *Account) GetGrokBaseURL() string {
	if base := strings.TrimSpace(a.GetCredential("base_url")); base != "" {
		return strings.TrimRight(base, "/")
	}
	return xai.DefaultBaseURL
}

// ---- Typed credential getters (Grok) ----

// GetGrokAccessToken returns the xAI OAuth access token (a plain Bearer to api.x.ai/v1).
func (a *Account) GetGrokAccessToken() string { return a.GetCredential("access_token") }

// GetGrokRefreshToken returns the xAI OAuth refresh token.
func (a *Account) GetGrokRefreshToken() string { return a.GetCredential("refresh_token") }

// GetGrokTokenEndpoint returns the cached OIDC token_endpoint (optional; empty
// triggers discovery on the next refresh).
func (a *Account) GetGrokTokenEndpoint() string { return a.GetCredential("token_endpoint") }

// GetGrokExpiresAt returns the access-token expiry, or nil if unset/unparseable.
func (a *Account) GetGrokExpiresAt() *time.Time { return a.GetCredentialAsTime("expires_at") }

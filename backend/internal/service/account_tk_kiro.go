package service

import (
	"strconv"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

// IsKiro reports whether the account is a Kiro (sixth platform) account.
// Kiro speaks the CodeWhisperer EventStream protocol (vendored in
// internal/integration/kiro) and is exposed through the native Anthropic
// /v1/messages path; it schedules in its own isolated pool and is
// intentionally NOT an OpenAI-compat member.
func (a *Account) IsKiro() bool {
	return a.Platform == PlatformKiro
}

// ---- Typed credential getters (Kiro) ----

// GetKiroAccessToken returns the Kiro OAuth access token.
func (a *Account) GetKiroAccessToken() string { return a.GetCredential("access_token") }

// GetKiroRefreshToken returns the Kiro OAuth refresh token.
func (a *Account) GetKiroRefreshToken() string { return a.GetCredential("refresh_token") }

// GetKiroProfileArn returns the Kiro CodeWhisperer profile ARN (optional).
func (a *Account) GetKiroProfileArn() string { return a.GetCredential("profile_arn") }

// GetKiroRegion returns the Kiro AWS region credential (may be empty).
func (a *Account) GetKiroRegion() string { return a.GetCredential("region") }

// GetKiroAuthMethod returns the Kiro auth method: "social" or "idc".
func (a *Account) GetKiroAuthMethod() string { return a.GetCredential("auth_method") }

// GetKiroMachineID returns the Kiro machine id (optional).
func (a *Account) GetKiroMachineID() string { return a.GetCredential("machine_id") }

// GetKiroClientID returns the Kiro IdC client id (required when auth_method=="idc").
func (a *Account) GetKiroClientID() string { return a.GetCredential("client_id") }

// GetKiroClientSecret returns the Kiro IdC client secret (required when auth_method=="idc").
func (a *Account) GetKiroClientSecret() string { return a.GetCredential("client_secret") }

// GetKiroExpiresAt returns the Kiro access-token expiry, or nil if unset/unparseable.
func (a *Account) GetKiroExpiresAt() *time.Time { return a.GetCredentialAsTime("expires_at") }

// kiroDefaultRegion is the fallback AWS region used when the account has no
// explicit region credential.
const kiroDefaultRegion = "us-east-1"

// toKiroProtoAccount maps the business Account onto the vendored kiroproto.Account
// shape consumed by internal/integration/kiro. The vendored ID field is a string,
// so the numeric account id is formatted as decimal. Region falls back to
// kiroDefaultRegion when empty.
func (a *Account) toKiroProtoAccount() *kiroproto.Account {
	if a == nil {
		return nil
	}
	region := a.GetKiroRegion()
	if region == "" {
		region = kiroDefaultRegion
	}
	proxyURL := ""
	if a.ProxyID != nil && a.Proxy != nil {
		proxyURL = a.Proxy.URL()
	}
	return &kiroproto.Account{
		ID:           strconv.FormatInt(a.ID, 10),
		AccessToken:  a.GetKiroAccessToken(),
		RefreshToken: a.GetKiroRefreshToken(),
		ProfileArn:   a.GetKiroProfileArn(),
		Region:       region,
		MachineId:    a.GetKiroMachineID(),
		AuthMethod:   a.GetKiroAuthMethod(),
		ClientID:     a.GetKiroClientID(),
		ClientSecret: a.GetKiroClientSecret(),
		ProxyURL:     proxyURL,
		Enabled:      true,
	}
}

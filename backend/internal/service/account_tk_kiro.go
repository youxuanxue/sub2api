package service

import (
	"strconv"
	"strings"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// IsKiro reports whether the account is a Kiro (sixth platform) account.
// Kiro speaks the CodeWhisperer EventStream protocol (vendored in
// internal/integration/kiro) and is exposed through the native Anthropic
// /v1/messages path; it schedules in its own isolated pool and is
// intentionally NOT an OpenAI-compat member.
func (a *Account) IsKiro() bool {
	return a.Platform == PlatformKiro
}

// IsKiroMirrorStub reports whether this account is a prod Anthropic API-key
// relay stub that represents a downstream edge Kiro pool.
func (a *Account) IsKiroMirrorStub() bool {
	if a == nil || a.Platform != PlatformAnthropic || a.Type != AccountTypeAPIKey {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a.GetCredential("mirror_platform")), PlatformKiro)
}

const KiroDefaultTestModel = "claude-sonnet-4-5"

// KiroAdminTestModels returns the safe client-facing model IDs for admin account
// tests. Kiro rejects dated Anthropic snapshot IDs such as
// claude-sonnet-4-5-20250929; the Kiro translator accepts the short IDs and
// normalizes them to the CodeWhisperer wire form.
func KiroAdminTestModels() []claude.Model {
	return []claude.Model{
		{
			ID:          KiroDefaultTestModel,
			Type:        "model",
			DisplayName: "Claude Sonnet 4.5",
			CreatedAt:   "",
		},
		{
			ID:          "claude-sonnet-4-6",
			Type:        "model",
			DisplayName: "Claude Sonnet 4.6",
			CreatedAt:   "",
		},
		{
			ID:          "claude-opus-4-8",
			Type:        "model",
			DisplayName: "Claude Opus 4.8",
			CreatedAt:   "",
		},
	}
}

// CanonicalKiroTLSProfileName is the name of the TLS fingerprint profile captured
// from a real Kiro IDE ClientHello (deploy/aws/stage0/tk_canonical_kiro_ide.json,
// seeded by migration tk_014). It is intentionally distinct from the Claude Code
// canonical profile (tk_canonical_cc_oauth): Kiro bundles Node 22.x while cc ships
// Node 24.x, so their JA3 differ and the profiles must not be shared.
const CanonicalKiroTLSProfileName = "tk_canonical_kiro_ide"

// isKiroTLSFingerprintEnabled reports whether TLS fingerprint masking is active for
// this Kiro account. It is default-on: Kiro egresses to AWS CodeWhisperer where a
// Go-default ClientHello stands out and raises the ban risk (the community-observed
// failure mode). Operators can opt out per-account by setting
// extra.enable_tls_fingerprint=false. The profile is resolved by name in
// TLSFingerprintProfileService.ResolveTLSProfile when no explicit
// extra.tls_fingerprint_profile_id is bound.
func (a *Account) isKiroTLSFingerprintEnabled() bool {
	if a.Extra != nil {
		if v, ok := a.Extra["enable_tls_fingerprint"]; ok {
			if enabled, ok := v.(bool); ok {
				return enabled
			}
		}
	}
	return true
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

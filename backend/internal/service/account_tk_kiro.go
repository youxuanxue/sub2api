package service

import (
	"context"
	"log/slog"
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
	if strings.EqualFold(strings.TrimSpace(a.GetCredential("mirror_platform")), PlatformKiro) {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(a.Name))
	if !strings.HasPrefix(name, "kiro-") {
		return false
	}
	baseURL := strings.TrimRight(strings.ToLower(strings.TrimSpace(a.GetCredential("base_url"))), "/")
	if baseURL != "" {
		return strings.HasPrefix(baseURL, "https://api-") && strings.HasSuffix(baseURL, ".tokenkey.dev")
	}
	// Scheduler snapshots intentionally carry only slim metadata. Older snapshots
	// may not include mirror_platform/base_url, but prod Kiro mirror stubs are
	// consistently named kiro-<edge>. Keep this as a narrow compatibility fallback
	// so model gating does not depend on cache hydration.
	return true
}

const KiroDefaultTestModel = "claude-sonnet-4-5"

// KiroAdminTestModels returns the empirically-servable client-facing Claude model
// IDs for admin account tests and mapping presets. Dated Anthropic snapshot IDs
// (e.g. claude-haiku-4-5-20251001) are normalized by the Kiro translator before
// upstream; this list intentionally exposes the short undated IDs operators should
// pick in the admin UI. Live probe source: ops/stage0/probe_kiro_claude_models.sh
// (edge us6 account 2 + prod mirror 66, 2026-07-02).
func KiroAdminTestModels() []claude.Model {
	return []claude.Model{
		{
			ID:          KiroDefaultTestModel,
			Type:        "model",
			DisplayName: "Claude Sonnet 4.5",
			CreatedAt:   "",
		},
		{
			ID:          "claude-haiku-4-5",
			Type:        "model",
			DisplayName: "Claude Haiku 4.5",
			CreatedAt:   "",
		},
		{
			ID:          "claude-sonnet-4-6",
			Type:        "model",
			DisplayName: "Claude Sonnet 4.6",
			CreatedAt:   "",
		},
		{
			ID:          "claude-sonnet-5",
			Type:        "model",
			DisplayName: "Claude Sonnet 5",
			CreatedAt:   "",
		},
		{
			ID:          "claude-opus-4-5",
			Type:        "model",
			DisplayName: "Claude Opus 4.5",
			CreatedAt:   "",
		},
		{
			ID:          "claude-opus-4-6",
			Type:        "model",
			DisplayName: "Claude Opus 4.6",
			CreatedAt:   "",
		},
		{
			ID:          "claude-opus-4-7",
			Type:        "model",
			DisplayName: "Claude Opus 4.7",
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

// kiroMirrorStubSupportsModel constrains prod Anthropic API-key mirror stubs
// that forward to the Kiro platform. They intentionally have empty
// credentials.model_mapping so older menu code could still show a useful
// catalog, but empty mapping must not mean "all Anthropic models" for routing:
// the downstream Kiro pool rejects Fable and older Opus ids even when CC API-key
// accounts in the same Anthropic group can serve them.
func kiroMirrorStubSupportsModel(requestedModel string) bool {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return true
	}
	normalized := claude.NormalizeModelID(requestedModel)
	for _, m := range KiroAdminTestModels() {
		if requestedModel == m.ID || normalized == m.ID {
			return true
		}
	}
	return false
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

// PersistKiroProfileArnIfChanged writes a freshly resolved profile_arn back to account
// credentials so subsequent usage/gateway calls do not repeat ListAvailableProfiles or
// keep sending a stale ARN that triggers HTTP 400 Invalid profileArn.
func PersistKiroProfileArnIfChanged(ctx context.Context, repo AccountRepository, account *Account, kiroAcct *kiroproto.Account) {
	if repo == nil || account == nil || kiroAcct == nil {
		return
	}
	resolved := strings.TrimSpace(kiroAcct.ProfileArn)
	if resolved == "" || resolved == account.GetKiroProfileArn() {
		return
	}
	merged := MergeCredentials(account.Credentials, map[string]any{"profile_arn": resolved})
	if err := persistAccountCredentials(ctx, repo, account, merged); err != nil {
		slog.Warn("persist_kiro_profile_arn_failed", "account_id", account.ID, "error", err)
	}
}

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

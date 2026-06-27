package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// tkAdminServiceGrokOAuth is the setter for the TK-specific Grok OAuth dependency.
// adminServiceImpl is defined in admin_service.go (upstream-shaped); adding a field
// to that struct would be a revert-risk edit every upstream merge. Instead we use a
// TK companion file (this one) with a separate field stored in a package-level map
// keyed by *adminServiceImpl pointer, avoiding any upstream-file modification while
// keeping the wiring explicit.
//
// In practice Wire resolves all providers before any service method is called, so the
// store will be populated before the first CreateAccount call that needs it.
var grokOAuthServiceStore = make(map[*adminServiceImpl]*GrokOAuthService)

// TkInjectGrokOAuthService injects the GrokOAuthService into the AdminService.
// Called once at Wire initialisation; Wire ensures this runs before any account-save.
func TkInjectGrokOAuthService(svc AdminService, grokOAuthSvc *GrokOAuthService) {
	if impl, ok := svc.(*adminServiceImpl); ok && impl != nil {
		grokOAuthServiceStore[impl] = grokOAuthSvc
	}
}

// tkGrokOAuthService retrieves the injected GrokOAuthService for this adminServiceImpl.
func (s *adminServiceImpl) tkGrokOAuthService() *GrokOAuthService {
	return grokOAuthServiceStore[s]
}

// resolveGrokTokenOnSave validates and primes a grok (seventh platform) OAuth
// account at create time. The operator pastes a refresh_token (minted out-of-band
// by the xAI Grok CLI loopback login — xAI's public client has no web-redirect /
// device-code flow, so server-side interactive OAuth is not possible), and
// TokenKey immediately exchanges it for a fresh access_token.
//
// This is the "paste just works" green check:
//   - success → the account is primed (access_token + expires_at + token_endpoint
//     persisted) and immediately schedulable;
//   - failure (missing/invalid refresh_token, or the SuperGrok-Heavy-only 403
//     entitlement gate, or a dead grant) → account creation is rejected with the
//     exact upstream reason, instead of materializing a dead account that only
//     fails on the first request.
//
// No-op for non-grok / non-oauth accounts. Mirrors resolveNewAPIMoonshotBaseURLOnSave
// (a create-time network call that mutates credentials before persistence).
func resolveGrokTokenOnSave(ctx context.Context, account *Account, grokOAuthSvc *GrokOAuthService) error {
	if account == nil || account.Platform != PlatformGrok || account.Type != AccountTypeOAuth {
		return nil
	}

	refreshToken := account.GetGrokRefreshToken()
	if refreshToken == "" {
		return fmt.Errorf("credentials.refresh_token is required for grok platform " +
			"(paste the refresh_token obtained from the xAI Grok CLI login)")
	}

	if grokOAuthSvc == nil {
		return fmt.Errorf("grok OAuth service unavailable")
	}

	tokenInfo, err := grokOAuthSvc.ValidateRefreshToken(ctx, refreshToken, account.ProxyID)
	if err != nil {
		return fmt.Errorf("grok refresh_token validation failed — the token must back a "+
			"SuperGrok Heavy account (xAI gates OAuth API access to Heavy): %w", err)
	}

	if account.Credentials == nil {
		account.Credentials = map[string]any{}
	}
	account.Credentials["access_token"] = tokenInfo.AccessToken
	if tokenInfo.ExpiresAt > 0 {
		account.Credentials["expires_at"] = time.Unix(tokenInfo.ExpiresAt, 0).UTC().Format(time.RFC3339)
	} else {
		account.Credentials["expires_at"] = time.Now().Add(grokDefaultAccessTokenTTL).UTC().Format(time.RFC3339)
	}
	if tokenInfo.RefreshToken != "" {
		account.Credentials["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.ClientID != "" {
		account.Credentials["client_id"] = tokenInfo.ClientID
	}
	return nil
}

// tkInputHasNonEmptyCredential reports whether the caller explicitly provided a
// non-empty value for the given credential key in this request. Used on the
// UpdateAccount path to gate the grok live re-validate: a blank refresh_token
// field means "keep current" (handled by MergePreservingSensitiveCreds + the
// background refresher), so an unrelated grok edit (concurrency / priority / …)
// must NOT fire an xAI refresh and must NOT be blocked by a transient xAI outage.
func tkInputHasNonEmptyCredential(creds map[string]any, key string) bool {
	if creds == nil {
		return false
	}
	if v, ok := creds[key].(string); ok {
		return strings.TrimSpace(v) != ""
	}
	return false
}

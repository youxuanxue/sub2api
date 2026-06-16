package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

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
func resolveGrokTokenOnSave(ctx context.Context, account *Account) error {
	if account == nil || account.Platform != PlatformGrok || account.Type != AccountTypeOAuth {
		return nil
	}

	refreshToken := account.GetGrokRefreshToken()
	if refreshToken == "" {
		return fmt.Errorf("credentials.refresh_token is required for grok platform " +
			"(paste the refresh_token obtained from the xAI Grok CLI login)")
	}

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	res, err := xai.RefreshToken(ctx, refreshToken, account.GetGrokTokenEndpoint(), proxyURL)
	if err != nil {
		return fmt.Errorf("grok refresh_token validation failed — the token must back a "+
			"SuperGrok Heavy account (xAI gates OAuth API access to Heavy): %w", err)
	}

	if account.Credentials == nil {
		account.Credentials = map[string]any{}
	}
	account.Credentials["access_token"] = res.AccessToken
	expiresIn := res.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = grokDefaultExpiresIn
	}
	account.Credentials["expires_at"] = strconv.FormatInt(
		time.Now().Add(time.Duration(expiresIn)*time.Second).Unix(), 10)
	if res.RefreshToken != "" {
		account.Credentials["refresh_token"] = res.RefreshToken
	}
	if res.TokenEndpoint != "" {
		account.Credentials["token_endpoint"] = res.TokenEndpoint
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

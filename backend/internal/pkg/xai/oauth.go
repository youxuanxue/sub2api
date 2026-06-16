// Package xai provides the minimal xAI / Grok (SuperGrok Heavy) OAuth refresh
// helper used by the grok seventh platform. It is intentionally dependency-free
// (only stdlib) so the service-layer GrokTokenRefresher can call it the same way
// the Kiro refresher calls the vendored kiroproto.RefreshToken.
//
// Scope of this package (MINIMAL-v1): token REFRESH only. The interactive
// authorization-code + PKCE loopback dance (the way an operator first mints a
// refresh_token) is performed out-of-band by the xAI Grok CLI on the operator's
// own machine; TokenKey only stores the resulting refresh_token and refreshes it
// server-side from here. The authorize/PKCE half is deferred to phase-2.
//
// OAuth contract (confirmed against the public xAI Grok CLI client, client_id
// b1a00492-073a-47ea-816f-4c329264a828):
//   - discovery: GET https://auth.x.ai/.well-known/openid-configuration -> token_endpoint
//   - refresh:   POST <token_endpoint> form {grant_type=refresh_token, client_id, refresh_token}
//     Content-Type: application/x-www-form-urlencoded ; no client_secret, no scope
//   - response:  {access_token, refresh_token (may be omitted -> keep old), expires_in (sec), ...}
//   - the access_token is a plain Bearer to api.x.ai/v1 (no Codex/ChatGPT headers).
package xai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ClientID is the public xAI Grok CLI OAuth client ID (not a secret).
	ClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	// Issuer is xAI's OAuth issuer.
	Issuer = "https://auth.x.ai"
	// DiscoveryURL is the OIDC discovery endpoint used to resolve the token endpoint.
	DiscoveryURL = Issuer + "/.well-known/openid-configuration"
	// DefaultAPIBaseURL is the default xAI (OpenAI-compatible) inference base URL.
	DefaultAPIBaseURL = "https://api.x.ai/v1"
	// Scope is the OAuth scope set xAI issues for Grok API access (for reference;
	// the refresh grant itself does not send a scope param).
	Scope = "openid profile email offline_access grok-cli:access api:access"

	// oauthHost is the trusted host suffix for OAuth endpoints (SSRF guard).
	oauthHost = "x.ai"
)

// RefreshLead is how long before access-token expiry a refresh should fire.
const RefreshLead = 5 * time.Minute

// TokenResult is the outcome of a refresh.
type TokenResult struct {
	AccessToken  string
	RefreshToken string // may be empty: when xAI omits rotation, caller keeps the old token
	ExpiresIn    int    // seconds
	// TokenEndpoint is the endpoint actually used; callers may cache it on the
	// account credentials to avoid re-running discovery on every refresh.
	TokenEndpoint string
}

func httpClient(proxyURL string) *http.Client {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if p := strings.TrimSpace(proxyURL); p != "" {
		if u, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}
}

// validateEndpoint rejects an OAuth endpoint that is not https on an x.ai host,
// guarding against a poisoned discovery document redirecting token POSTs.
func validateEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("xai oauth: invalid endpoint %q", raw)
	}
	host := u.Hostname()
	if host != oauthHost && !strings.HasSuffix(host, "."+oauthHost) {
		return "", fmt.Errorf("xai oauth: endpoint host %q not under %s", host, oauthHost)
	}
	return raw, nil
}

// Discover resolves the token_endpoint from xAI OIDC discovery.
func Discover(ctx context.Context, proxyURL string) (tokenEndpoint string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DiscoveryURL, nil)
	if err != nil {
		return "", fmt.Errorf("xai oauth discovery: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient(proxyURL).Do(req)
	if err != nil {
		return "", fmt.Errorf("xai oauth discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("xai oauth discovery: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("xai oauth discovery: decode: %w", err)
	}
	return validateEndpoint(payload.TokenEndpoint)
}

// RefreshToken exchanges a refresh_token for a fresh access token. When
// tokenEndpoint is empty it is resolved via Discover first. A non-2xx response
// (including invalid_grant / 403 entitlement gating) is returned as an error
// whose text includes the upstream body, so the service layer can classify
// rolling-revocation and the Heavy-only entitlement gate.
func RefreshToken(ctx context.Context, refreshToken, tokenEndpoint, proxyURL string) (*TokenResult, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("xai oauth refresh: refresh_token is required")
	}

	endpoint := strings.TrimSpace(tokenEndpoint)
	if endpoint == "" {
		discovered, err := Discover(ctx, proxyURL)
		if err != nil {
			return nil, err
		}
		endpoint = discovered
	} else {
		validated, err := validateEndpoint(endpoint)
		if err != nil {
			return nil, err
		}
		endpoint = validated
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("xai oauth refresh: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient(proxyURL).Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai oauth refresh: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Preserve the upstream body verbatim: invalid_grant (dead grant ->
		// re-auth, never loop) and 403 (Heavy-only entitlement gate) are
		// classified downstream by their text.
		return nil, fmt.Errorf("xai oauth refresh: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("xai oauth refresh: decode: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("xai oauth refresh: empty access_token in response")
	}
	return &TokenResult{
		AccessToken:   strings.TrimSpace(payload.AccessToken),
		RefreshToken:  strings.TrimSpace(payload.RefreshToken),
		ExpiresIn:     payload.ExpiresIn,
		TokenEndpoint: endpoint,
	}, nil
}

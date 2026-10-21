package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var oidcTokenURL = func(region string) string { return "https://oidc." + region + ".amazonaws.com/token" }
var socialTokenURL = func() string { return "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken" }
var kiroRegionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]{1,2}$`)

// RefreshTokenContext is called only by the credential-refresh owner. Profile
// discovery must never rotate credentials outside its lock/persistence boundary.
func RefreshTokenContext(ctx context.Context, account *Account) (string, string, int64, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", 0, "", err
	}
	if account == nil || strings.TrimSpace(account.RefreshToken) == "" {
		return "", "", 0, "", fmt.Errorf("Kiro refresh token is required")
	}
	endpoint := socialTokenURL()
	expectedHost := "prod.us-east-1.auth.desktop.kiro.dev"
	payload := map[string]string{"refreshToken": account.RefreshToken}
	if account.AuthMethod != "social" {
		if strings.TrimSpace(account.ClientID) == "" || strings.TrimSpace(account.ClientSecret) == "" {
			return "", "", 0, "", fmt.Errorf("OIDC refresh requires clientId and clientSecret")
		}
		region := account.Region
		if region == "" {
			region = "us-east-1"
		}
		if len(region) > 32 || !kiroRegionPattern.MatchString(region) {
			return "", "", 0, "", fmt.Errorf("invalid Kiro OIDC region")
		}
		endpoint = oidcTokenURL(region)
		expectedHost = "oidc." + region + ".amazonaws.com"
		payload["clientId"], payload["clientSecret"], payload["grantType"] = account.ClientID, account.ClientSecret, "refresh_token"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host != expectedHost || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", 0, "", fmt.Errorf("invalid Kiro refresh endpoint")
	}
	// Copy the cached client to avoid mutating shared redirect policy.
	client := *GetAuthClientForProxy(ResolveAccountProxyURL(account))
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return "", "", 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, "", err
	}
	defer resp.Body.Close()
	// Bound authentication responses and never expose their raw credential fields.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return "", "", 0, "", err
	}
	if len(raw) > 1<<20 {
		return "", "", 0, "", fmt.Errorf("Kiro refresh response too large")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &failure)
		if failure.Error == "invalid_grant" {
			return "", "", 0, "", fmt.Errorf("Kiro refresh failed: %d invalid_grant", resp.StatusCode)
		}
		return "", "", 0, "", fmt.Errorf("Kiro refresh failed: %d", resp.StatusCode)
	}
	var result struct {
		AccessToken  string  `json:"accessToken"`
		RefreshToken *string `json:"refreshToken"`
		ExpiresIn    int64   `json:"expiresIn"`
		ProfileArn   string  `json:"profileArn"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", "", 0, "", fmt.Errorf("invalid Kiro refresh response JSON")
	}
	now := time.Now().Unix()
	if strings.TrimSpace(result.AccessToken) == "" || result.ExpiresIn <= 0 || result.ExpiresIn > math.MaxInt64-now {
		return "", "", 0, "", fmt.Errorf("invalid Kiro refresh access token or expiry")
	}
	refresh := account.RefreshToken
	if result.RefreshToken != nil {
		if strings.TrimSpace(*result.RefreshToken) == "" {
			return "", "", 0, "", fmt.Errorf("invalid Kiro refresh token")
		}
		refresh = *result.RefreshToken
	}
	if err := ctx.Err(); err != nil {
		return "", "", 0, "", err
	}
	return result.AccessToken, refresh, now + result.ExpiresIn, result.ProfileArn, nil
}

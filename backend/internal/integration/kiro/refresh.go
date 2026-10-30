package kiro

import (
	"bytes"
	"context"
	"encoding/base64"
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
		if kiroIDCClientSecretExpired(account.ClientSecret, time.Now()) {
			// The OIDC host answers this with a bare HTTP 400. Classify it
			// here so callers can take this account out of the pool without
			// sending a dead client secret. The phrase must not contain
			// invalid_client or unauthorized_client: the background refresher
			// treats those OAuth codes as a provider-wide configuration fault,
			// leaves the account schedulable, and stops the rest of the cycle.
			return "", "", 0, "", fmt.Errorf("Kiro refresh failed: client secret expired")
		}
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
		return "", "", 0, "", kiroRefreshHTTPError(resp.StatusCode, raw)
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

// kiroRefreshHTTPError classifies a non-200 refresh response without echoing
// credential fields. invalid_grant, and a refresh token the auth host says is
// revoked or rejected, are terminal: callers must not send that token again.
func kiroRefreshHTTPError(status int, raw []byte) error {
	var failure struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Message          string `json:"message"`
		Reason           string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &failure)
	blob := strings.ToLower(strings.Join([]string{
		failure.Error,
		failure.ErrorDescription,
		failure.Message,
		failure.Reason,
	}, " "))
	if strings.Contains(blob, "invalid_grant") ||
		(strings.Contains(blob, "refresh") && (strings.Contains(blob, "revoked") || strings.Contains(blob, "rejected"))) {
		return fmt.Errorf("Kiro refresh failed: %d invalid_grant", status)
	}
	switch failure.Error {
	case "invalid_client", "unauthorized_client":
		// Kiro IDC client credentials belong to one account. Echoing the
		// OAuth code would trip the shared-provider breaker instead of
		// marking this account terminal. See the precheck above.
		return fmt.Errorf("Kiro refresh failed: %d client secret expired", status)
	}
	return fmt.Errorf("Kiro refresh failed: %d", status)
}

// kiroIDCClientSecretExpired reports whether an AWS SSO OIDC client secret can
// no longer refresh. Kiro Builder ID registrations last about 90 days;
// expirationTimestamp is carried inside the client-secret JWT. The sibling
// isExpired claim is stamped at registration and stays false after expiry, so
// only a parsed timestamp in the past counts. Unparseable secrets fail open
// and still go to the token endpoint.
func kiroIDCClientSecretExpired(secret string, now time.Time) bool {
	exp, ok := kiroClientSecretExpiration(secret)
	if !ok {
		return false
	}
	return !exp.After(now)
}

func kiroClientSecretExpiration(secret string) (time.Time, bool) {
	parts := strings.Split(secret, ".")
	if len(parts) != 3 || parts[1] == "" {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}, false
		}
	}
	var wrapper struct {
		Serialized string `json:"serialized"`
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil || strings.TrimSpace(wrapper.Serialized) == "" {
		return time.Time{}, false
	}
	decoder := json.NewDecoder(strings.NewReader(wrapper.Serialized))
	decoder.UseNumber()
	var claims struct {
		ExpirationTimestamp json.Number `json:"expirationTimestamp"`
	}
	if err := decoder.Decode(&claims); err != nil {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(claims.ExpirationTimestamp.String())
	if raw == "" {
		return time.Time{}, false
	}
	n, err := claims.ExpirationTimestamp.Int64()
	if err != nil {
		f, ferr := claims.ExpirationTimestamp.Float64()
		if ferr != nil {
			return time.Time{}, false
		}
		n = int64(f)
	}
	switch {
	case n > 1_000_000_000_000:
		return time.UnixMilli(n), true
	case n > 1_000_000_000:
		return time.Unix(n, 0), true
	default:
		return time.Time{}, false
	}
}

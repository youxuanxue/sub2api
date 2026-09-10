package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const OAuthClientID = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"

// Cursor Desktop renews through /oauth/token and stores the returned access
// token as the next refresh grant. This also accepts CLI login access tokens.
func RefreshOAuth(ctx context.Context, token string, do func(*http.Request) (*http.Response, error)) (*OAuthCredentials, error) {
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("missing or invalid Cursor renewal token")
	}
	body, err := json.Marshal(map[string]string{"grant_type": "refresh_token", "client_id": OAuthClientID, "refresh_token": token})
	if err != nil {
		return nil, errors.New("encode Cursor renewal request")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthAPIBaseURL+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("create Cursor renewal request")
	}
	setOAuthClientHeaders(req)
	resp, err := do(req)
	if err != nil {
		return nil, errors.New("cursor token refresh transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	var wire struct {
		AccessToken  string `json:"access_token"`
		ShouldLogout bool   `json:"shouldLogout"`
		Error        string `json:"error"`
	}
	decodeErr := decodeOAuthResponse(resp.Body, &wire)
	if (resp.StatusCode == http.StatusOK && wire.ShouldLogout) ||
		(resp.StatusCode == http.StatusBadRequest && wire.Error == "invalid_grant") {
		return nil, errors.New("cursor token refresh invalid_grant: browser reauthorization required")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("cursor token refresh returned HTTP %d", resp.StatusCode)}
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	expires, err := OAuthTokenExpiry(wire.AccessToken)
	if err != nil || !expires.After(time.Now()) {
		return nil, errors.New("cursor token refresh returned an invalid or expired token")
	}
	return &OAuthCredentials{AccessToken: wire.AccessToken, RefreshToken: wire.AccessToken, ExpiresAt: expires}, nil
}

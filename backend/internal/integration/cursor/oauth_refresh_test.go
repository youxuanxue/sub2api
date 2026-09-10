package cursor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func renewalTestToken(expires time.Time) string {
	return "test." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expires.Unix()))) + ".signature"
}

func TestRefreshOAuthDesktopContractAndRenewalChain(t *testing.T) {
	next := renewalTestToken(time.Now().Add(time.Hour))
	grant := "original-secret"
	for range 2 {
		result, err := RefreshOAuth(context.Background(), grant, func(req *http.Request) (*http.Response, error) {
			require.Equal(t, OAuthAPIBaseURL+"/oauth/token", req.URL.String())
			require.Equal(t, http.MethodPost, req.Method)
			require.Equal(t, "cli", req.Header.Get("X-Cursor-Client-Type"))
			var body map[string]string
			require.NoError(t, json.NewDecoder(req.Body).Decode(&body))
			require.Equal(t, map[string]string{"grant_type": "refresh_token", "client_id": OAuthClientID, "refresh_token": grant}, body)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"access_token":%q,"shouldLogout":false}`, next)))}, nil
		})
		require.NoError(t, err)
		require.Equal(t, next, result.AccessToken)
		require.Equal(t, result.AccessToken, result.RefreshToken)
		grant = result.RefreshToken
	}
}

func TestRefreshOAuthRejectsInvalidResultsWithoutLeakingSecrets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"logout", 200, `{"shouldLogout":true,"access_token":"secret"}`, "invalid_grant"},
		{"revoked", 400, `{"error":"invalid_grant","error_description":"secret"}`, "invalid_grant"},
		{"unavailable", 503, `secret`, "HTTP 503"},
		{"invalid", 200, `{"access_token":"secret"}`, "invalid or expired"},
		{"missing", 200, `{}`, "invalid or expired"},
		{"malformed", 200, `{`, "invalid Cursor authorization response"},
		{"expired", 200, fmt.Sprintf(`{"access_token":%q}`, renewalTestToken(time.Now().Add(-time.Hour))), "invalid or expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := RefreshOAuth(t.Context(), "secret", func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			require.Nil(t, result)
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

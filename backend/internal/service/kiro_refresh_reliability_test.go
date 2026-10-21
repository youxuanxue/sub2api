//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	kiro "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

type riskAuthTransport func(*http.Request) (*http.Response, error)

func (f riskAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestKiroReliabilityRefreshService(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		cancel     bool
	}{
		{"omitted_refresh", `{"accessToken":"new","expiresIn":3600}`, false},
		{"empty_access", `{}`, false},
		{"negative_expiry", `{"accessToken":"new","refreshToken":"rotated","expiresIn":-1}`, false},
		{"cancelled", `{"accessToken":"new","refreshToken":"rotated","expiresIn":3600}`, true},
		{"valid_rotation", `{"accessToken":"new","refreshToken":"rotated","expiresIn":3600}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			requestCancelled := false
			client := kiro.GetAuthClientForProxy("")
			saved := *client
			defer func() { *client = saved }()
			client.Transport = riskAuthTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				requestCancelled = req.Context().Err() != nil
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}}, nil
			})
			a := newKiroAccountForTest()
			a.Type = AccountTypeOAuth
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			got, err := NewKiroTokenRefresher().Refresh(ctx, a)
			switch tc.name {
			case "omitted_refresh":
				if err != nil || got["refresh_token"] != "rt" {
					t.Error("refresh merge loses old refresh token")
				}
			case "empty_access", "negative_expiry":
				if err == nil {
					t.Error("invalid credentials accepted by service")
				}
			case "cancelled":
				if err == nil || calls > 0 && !requestCancelled {
					t.Error("cancelled refresh reaches transport without cancellation")
				}
			case "valid_rotation":
				if err != nil || got["access_token"] != "new" || got["refresh_token"] != "rotated" {
					t.Error("valid control failed")
				}
			}
		})
	}
}

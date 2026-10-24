//go:build unit

package kiro

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type refreshTransport func(*http.Request) (*http.Response, error)

func (f refreshTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func installRefreshClient(t *testing.T, client *http.Client) *Account {
	t.Helper()
	key := "http://" + strings.ReplaceAll(t.Name(), "/", "-") + ".invalid:9999"
	authProxyClientCache.Store(key, client)
	t.Cleanup(func() { authProxyClientCache.Delete(key) })
	return &Account{AuthMethod: "social", RefreshToken: "old-refresh", ProxyURL: key}
}

func TestKiroReliabilityRefreshValidation(t *testing.T) {
	for _, method := range []string{"social", "idc"} {
		for _, tc := range []struct {
			name, body, wantRefresh string
			valid                   bool
		}{
			{"omitted refresh", `{"accessToken":"new-access","expiresIn":3600}`, "old-refresh", true},
			{"rotation", `{"accessToken":"new-access","refreshToken":"rotated","expiresIn":3600}`, "rotated", true},
			{"empty access", `{"accessToken":"","expiresIn":3600}`, "", false},
			{"missing expiry", `{"accessToken":"new-access"}`, "", false},
			{"negative expiry", `{"accessToken":"new-access","expiresIn":-1}`, "", false},
			{"overflow expiry", `{"accessToken":"new-access","expiresIn":9223372036854775807}`, "", false},
			{"empty refresh", `{"accessToken":"new-access","refreshToken":"","expiresIn":3600}`, "", false},
			{"trailing JSON", `{"accessToken":"new-access","expiresIn":3600}{}`, "", false},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				a := installRefreshClient(t, &http.Client{Transport: refreshTransport(func(r *http.Request) (*http.Response, error) {
					var body map[string]string
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Equal(t, "old-refresh", body["refreshToken"])
					require.Equal(t, "https", r.URL.Scheme)
					if method == "idc" {
						require.Equal(t, "oidc.eu-west-1.amazonaws.com", r.URL.Host)
						require.Equal(t, "secret", body["clientSecret"])
					} else {
						require.Equal(t, "prod.us-east-1.auth.desktop.kiro.dev", r.URL.Host)
					}
					return restTestResponse(200, tc.body), nil
				})})
				a.AuthMethod = method
				a.ClientID = "id"
				a.ClientSecret = "secret"
				a.Region = "eu-west-1"
				access, refresh, expiry, _, err := RefreshTokenContext(context.Background(), a)
				if tc.valid {
					require.NoError(t, err)
					require.Equal(t, "new-access", access)
					require.Equal(t, tc.wantRefresh, refresh)
					require.InDelta(t, time.Now().Unix()+3600, expiry, 2)
				} else {
					require.Error(t, err)
					require.Empty(t, access)
					require.Empty(t, refresh)
					require.Zero(t, expiry)
				}
				require.Equal(t, "old-refresh", a.RefreshToken, "only the service owner may persist credentials")
			})
		}
	}
}

func TestKiroReliabilityRefreshRegionAndRedirect(t *testing.T) {
	for _, region := range []string{"us-east-1@attacker.invalid/", "us-east-1:443", "../x", "us-east-1?x", "us-east-1#x", "us-east-1\n", "US-EAST-1", strings.Repeat("a", 64)} {
		t.Run(region, func(t *testing.T) {
			calls := 0
			a := installRefreshClient(t, &http.Client{Transport: refreshTransport(func(*http.Request) (*http.Response, error) { calls++; return restTestResponse(200, `{}`), nil })})
			a.AuthMethod = "idc"
			a.ClientID = "id"
			a.ClientSecret = "secret"
			a.Region = region
			_, _, _, _, err := RefreshTokenContext(context.Background(), a)
			require.Error(t, err)
			require.Zero(t, calls)
		})
	}
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			// Real net/http redirect handling runs against a local TLS server. The
			// transport rewrites only the initial host; any redirected request is counted.
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				http.Redirect(w, r, "https://credential-sink.invalid/", status)
			}))
			defer server.Close()
			transport := server.Client().Transport
			a := installRefreshClient(t, &http.Client{Transport: refreshTransport(func(r *http.Request) (*http.Response, error) {
				clone := r.Clone(r.Context())
				clone.URL.Host = strings.TrimPrefix(server.URL, "https://")
				return transport.RoundTrip(clone)
			})})
			_, _, _, _, err := RefreshTokenContext(context.Background(), a)
			require.Error(t, err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestKiroReliabilityRefreshCancellation(t *testing.T) {
	for _, cancelBefore := range []bool{true, false} {
		t.Run(map[bool]string{true: "before", false: "during"}[cancelBefore], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			a := installRefreshClient(t, &http.Client{Transport: refreshTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				cancel()
				<-r.Context().Done()
				return nil, r.Context().Err()
			})})
			if cancelBefore {
				cancel()
			}
			_, _, _, _, err := RefreshTokenContext(ctx, a)
			require.ErrorIs(t, err, context.Canceled)
			if cancelBefore {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestKiroReliabilityProfileDoesNotRotateCredentials(t *testing.T) {
	refreshes := 0
	a := installRefreshClient(t, &http.Client{Transport: refreshTransport(func(*http.Request) (*http.Response, error) {
		refreshes++
		return restTestResponse(200, `{"accessToken":"rotated","refreshToken":"rotated","expiresIn":3600,"profileArn":"arn:new"}`), nil
	})})
	a.AccessToken = "old-access"
	_, err := ResolveProfileArnWithDoer(a, restDoerFunc(func(*http.Request) (*http.Response, error) {
		return restTestResponse(401, `{"message":"Unauthorized"}`), nil
	}))
	require.Error(t, err)
	require.Zero(t, refreshes)
	require.Equal(t, "old-access", a.AccessToken)
	require.Equal(t, "old-refresh", a.RefreshToken)
}

func TestKiroReliabilityRefreshErrorsDoNotLeakCredentials(t *testing.T) {
	a := installRefreshClient(t, &http.Client{Transport: refreshTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant","refreshToken":"secret-canary"}`))}, nil
	})})
	_, _, _, _, err := RefreshTokenContext(context.Background(), a)
	require.ErrorContains(t, err, "invalid_grant")
	require.NotContains(t, err.Error(), "secret-canary")
}

func TestKiroRefreshHTTPError_RevokedRefreshIsTerminal(t *testing.T) {
	err := kiroRefreshHTTPError(400, []byte(`{"error":"access_denied","error_description":"Refresh token has been revoked"}`))
	require.ErrorContains(t, err, "invalid_grant")
	require.ErrorContains(t, err, "400")

	transient := kiroRefreshHTTPError(503, []byte(`{"message":"temporarily unavailable"}`))
	require.ErrorContains(t, transient, "503")
	require.NotContains(t, transient.Error(), "invalid_grant")
}

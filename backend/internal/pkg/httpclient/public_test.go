package httpclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPublicClientRejectsLocalTargetsAndRedirects(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer target.Close()
	client := NewPublicClient(time.Second)
	defer client.CloseIdleConnections()
	_, err := client.Get(target.URL)
	require.ErrorContains(t, err, "non-public")
	require.Zero(t, hits)
	// Inject only the first response; the redirected request still passes through
	// the production transport and must not reach the local server.
	guarded := client.Transport
	client.Transport = publicTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "public.example" {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{target.URL}}, Body: http.NoBody, Request: req}, nil
		}
		return guarded.RoundTrip(req)
	})
	_, err = client.Get("https://public.example/image")
	require.ErrorContains(t, err, "non-public")
	require.Zero(t, hits)
}

type publicTestRoundTripper func(*http.Request) (*http.Response, error)

func (f publicTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicDialPinsResolvedAddressAndRejectsMixedAnswers(t *testing.T) {
	for _, ips := range [][]string{{"8.8.8.8"}, {"8.8.8.8", "127.0.0.1"}, {"::ffff:127.0.0.1"}, {"169.254.169.254"}, {"100.64.0.1"}, {"fc00::1"}, {"0.0.0.0"}, {}} {
		lookups, dials := 0, 0
		fn := publicDialContext(func(context.Context, string) ([]net.IPAddr, error) {
			lookups++
			out := make([]net.IPAddr, len(ips))
			for i, ip := range ips {
				out[i].IP = net.ParseIP(ip)
			}
			return out, nil
		}, func(_ context.Context, _, address string) (net.Conn, error) {
			dials++
			require.Equal(t, "8.8.8.8:443", address, "never dial the original DNS name again")
			return nil, errors.New("synthetic dial, no network")
		})
		_, err := fn(context.Background(), "tcp", "public.example:443")
		require.Error(t, err)
		require.Equal(t, 1, lookups)
		if len(ips) == 1 && ips[0] == "8.8.8.8" {
			require.Equal(t, 1, dials)
		} else {
			require.Zero(t, dials)
		}
	}
}

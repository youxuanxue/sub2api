//go:build unit

package repository

import (
	"context"
	"crypto/tls"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newTLSIdentityTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func tlsIdentityEvents(t *testing.T, logs *observer.ObservedLogs) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range logs.FilterMessage(service.UpstreamTLSIdentityMessage).All() {
		out = append(out, e.ContextMap())
	}
	return out
}

func doAndDrain(t *testing.T, do func(*http.Request) (*http.Response, error), req *http.Request) {
	t.Helper()
	resp, err := do(req)
	require.NoError(t, err)
	_, _ = io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
}

// A flagged OpenAI OAuth account goes out through the uTLS fingerprint
// transport with the Codex CLI profile and negotiates HTTP/2.
func TestDoWithTLSRecordsCodexProfileOverHTTP2(t *testing.T) {
	srv := newTLSIdentityTestServer(t)
	core, logs := observer.New(zap.InfoLevel)
	const accountID = int64(910001)
	// prod enables gateway.openai_http2; with it off the OpenAI fingerprint
	// transport is HTTP/1.1 only.
	s, ok := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{Enabled: true}}}).(*httpUpstreamService)
	require.True(t, ok)
	profile := &tlsfingerprint.Profile{Name: service.CanonicalCodexCLITLSProfileName, ALPNProtocols: []string{"h2", "http/1.1"}}

	entry, err := s.getClientEntryWithTLS("", accountID, 1, profile, service.HTTPUpstreamProfileOpenAI, false, true)
	require.NoError(t, err)
	tr, ok := entry.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, tr.DialTLSContext, "fingerprint transport owns the TLS dial")
	// uTLS cannot trust the httptest certificate; keep the real fingerprint
	// transport (h2 settings included) and swap only the handshake.
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := (&tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, NextProtos: profile.ALPNProtocols}}).DialContext(ctx, network, addr) // #nosec G402 -- test fixture
		if err != nil {
			return nil, err
		}
		return nonStdTLSConn{Conn: raw}, nil
	}
	t.Cleanup(tr.CloseIdleConnections)

	ctx := logger.IntoContext(service.WithHTTPUpstreamProfile(t.Context(), service.HTTPUpstreamProfileOpenAI), zap.New(core))
	for i := 0; i < 3; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/backend-api/codex/responses", nil)
		require.NoError(t, err)
		doAndDrain(t, func(r *http.Request) (*http.Response, error) {
			return s.DoWithTLS(r, "", accountID, 1, profile)
		}, req)
	}

	events := tlsIdentityEvents(t, logs)
	require.Len(t, events, 1, "repeat requests on the same identity are rate-limited")
	ev := events[0]
	require.Equal(t, service.UpstreamTLSIdentityComponent, ev["component"])
	require.Equal(t, accountID, ev["account_id"])
	require.Equal(t, "https", ev["transport"])
	require.Equal(t, "utls", ev["tls_stack"])
	require.Equal(t, service.CanonicalCodexCLITLSProfileName, ev["tls_profile"])
	require.Equal(t, "HTTP/2.0", ev["http_proto"])
	require.Equal(t, "h2", ev["alpn"])
	require.Equal(t, srv.Listener.Addr().String(), ev["upstream_host"])
}

// A non-flagged account uses crypto/tls; tls_profile stays empty.
func TestDoRecordsStdlibTLSForNonFlaggedAccount(t *testing.T) {
	srv := newTLSIdentityTestServer(t)
	core, logs := observer.New(zap.InfoLevel)
	const accountID = int64(910002)
	s, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
	require.True(t, ok)
	entry, err := s.getClientEntry("", accountID, 1, service.HTTPUpstreamProfileDefault, false, true)
	require.NoError(t, err)
	base, ok := srv.Client().Transport.(*http.Transport)
	require.True(t, ok)
	entry.client = &http.Client{Transport: base.Clone()}

	req, err := http.NewRequestWithContext(logger.IntoContext(t.Context(), zap.New(core)), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	doAndDrain(t, func(r *http.Request) (*http.Response, error) { return s.Do(r, "", accountID, 1) }, req)

	events := tlsIdentityEvents(t, logs)
	require.Len(t, events, 1)
	require.Equal(t, "crypto/tls", events[0]["tls_stack"])
	require.Equal(t, "", events[0]["tls_profile"])
	require.Equal(t, "HTTP/2.0", events[0]["http_proto"])
	require.Equal(t, "h2", events[0]["alpn"])
}

// A configured profile whose cached transport has no uTLS dialer (e.g. the
// https:// proxy fallback) must be reported as crypto/tls, not as the profile.
func TestDoWithTLSReportsStdlibWhenTransportFellBack(t *testing.T) {
	srv := newTLSIdentityTestServer(t)
	core, logs := observer.New(zap.InfoLevel)
	_, do := lifecycleClient(t, srv, true) // replaces the TLS entry with a stock transport
	req, err := http.NewRequestWithContext(logger.IntoContext(t.Context(), zap.New(core)), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	doAndDrain(t, do, req)

	events := tlsIdentityEvents(t, logs)
	require.Len(t, events, 1)
	require.Equal(t, "crypto/tls", events[0]["tls_stack"])
	require.Equal(t, "", events[0]["tls_profile"])
}

func TestDoWithTLSProxyHostOmitsCredentials(t *testing.T) {
	require.False(t, upstreamEntryUsesTLSFingerprint(nil))
	require.False(t, upstreamEntryUsesTLSFingerprint(&upstreamClientEntry{client: &http.Client{}}))
	id := service.UpstreamTLSIdentityFromResponse(1, "https", "", "http://user:pass@proxy.test:3128/p?token=x", &http.Response{Proto: "HTTP/1.1"})
	require.Equal(t, "proxy.test:3128", id.ProxyHost)
	require.Equal(t, "none", id.TLSStack)
}

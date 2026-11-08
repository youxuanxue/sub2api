//go:build unit

package service

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestUpstreamTLSIdentityLogIsRateLimitedPerKey(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	now := time.Unix(1_000, 0)
	l := newUpstreamTLSIdentityLimiter(10*time.Minute, 2, func() time.Time { return now })
	a := UpstreamTLSIdentity{AccountID: 1, Transport: "https", TLSStack: "utls", TLSProfile: "p", HTTPProto: "HTTP/2.0", ALPN: "h2"}

	for i := 0; i < 5; i++ {
		recordUpstreamTLSIdentity(ctx, l, a)
	}
	require.Equal(t, 1, logs.FilterMessage(UpstreamTLSIdentityMessage).Len(), "same identity within interval logs once")

	// A different negotiated protocol for the same account is a new fact.
	b := a
	b.HTTPProto, b.ALPN = "HTTP/1.1", "http/1.1"
	recordUpstreamTLSIdentity(ctx, l, b)
	require.Equal(t, 2, logs.FilterMessage(UpstreamTLSIdentityMessage).Len())

	now = now.Add(10 * time.Minute)
	recordUpstreamTLSIdentity(ctx, l, a)
	all := logs.FilterMessage(UpstreamTLSIdentityMessage).All()
	require.Len(t, all, 3)
	require.Equal(t, int64(4), all[2].ContextMap()["suppressed_since_last"], "re-emission reports suppressed count")
	require.Equal(t, int64(600), all[2].ContextMap()["log_interval_sec"])

	// Key cap: stale keys are pruned, map never exceeds maxKeys.
	c := a
	c.AccountID = 3
	recordUpstreamTLSIdentity(ctx, l, c)
	require.LessOrEqual(t, len(l.entries), 2)

	// Missing account / stack are ignored.
	recordUpstreamTLSIdentity(ctx, l, UpstreamTLSIdentity{TLSStack: "utls"})
	recordUpstreamTLSIdentity(ctx, l, UpstreamTLSIdentity{AccountID: 9})
	require.Equal(t, 4, logs.FilterMessage(UpstreamTLSIdentityMessage).Len())
}

func TestUpstreamTLSIdentityFromResponseUsesNegotiatedState(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/responses?token=x", nil)
	h2 := &http.Response{Proto: "HTTP/2.0", Request: req, TLS: &tls.ConnectionState{NegotiatedProtocol: "h2"}}

	id := UpstreamTLSIdentityFromResponse(7, UpstreamTLSIdentityTransportHTTPS, CanonicalCodexCLITLSProfileName, "socks5://u:p@10.0.0.1:1080", h2)
	require.Equal(t, UpstreamTLSIdentity{AccountID: 7, Transport: "https", TLSStack: "utls", TLSProfile: CanonicalCodexCLITLSProfileName,
		HTTPProto: "HTTP/2.0", ALPN: "h2", Host: "chatgpt.com", ProxyHost: "10.0.0.1:1080"}, id)

	id = UpstreamTLSIdentityFromResponse(7, UpstreamTLSIdentityTransportHTTPS, "", "", h2)
	require.Equal(t, "crypto/tls", id.TLSStack)
	require.Empty(t, id.TLSProfile)

	plain := &http.Response{Proto: "HTTP/1.1", Request: req}
	id = UpstreamTLSIdentityFromResponse(7, UpstreamTLSIdentityTransportHTTPS, "p", "", plain)
	require.Equal(t, "none", id.TLSStack)
	require.Empty(t, id.TLSProfile, "no handshake means the profile was not used")
}

func TestUpstreamTLSIdentityEventIsIndexedIntoOpsSystemLogs(t *testing.T) {
	sink := &OpsSystemLogSink{}
	require.True(t, sink.shouldIndex(&logger.LogEvent{
		Level:  "info",
		Fields: map[string]any{"component": UpstreamTLSIdentityComponent},
	}), "Info-level audit.* events must reach ops_system_logs")
}

func newTLSIdentityWSServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close(coderws.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wssURL(srv *httptest.Server) string { return "wss" + srv.URL[len("https"):] }

// A flagged account's WSS upgrade uses the uTLS client (ALPN stripped) and
// stays HTTP/1.1.
func TestOpenAIWSDialRecordsProfileOverHTTP1(t *testing.T) {
	srv := newTLSIdentityWSServer(t)
	core, logs := observer.New(zap.InfoLevel)
	profile := &tlsfingerprint.Profile{Name: CanonicalCodexCLITLSProfileName, ALPNProtocols: []string{"h2", "http/1.1"}}
	account := &Account{ID: 920001}
	ctx := withOpenAIWSDialIdentity(logger.IntoContext(t.Context(), zap.New(core)), account, profile)

	d := &coderOpenAIWSClientDialer{proxyClients: map[string]*openAIWSProxyClientEntry{}}
	client, ok, err := d.tkOpenAIWSTLSHTTPClient(ctx, "")
	require.NoError(t, err)
	require.True(t, ok)
	tr := client.Transport.(*http.Transport)
	// uTLS cannot trust the httptest certificate; swap only the handshake and
	// mirror the WS-derived hello (no ALPN extension).
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}}).DialContext(ctx, network, addr) // #nosec G402 -- test fixture
	}
	t.Cleanup(tr.CloseIdleConnections)

	conn, _, _, err := d.Dial(ctx, wssURL(srv), http.Header{}, "")
	require.NoError(t, err)
	_ = conn.Close()

	events := logs.FilterMessage(UpstreamTLSIdentityMessage).All()
	require.Len(t, events, 1)
	ev := events[0].ContextMap()
	require.Equal(t, int64(920001), ev["account_id"])
	require.Equal(t, "wss", ev["transport"])
	require.Equal(t, "utls", ev["tls_stack"])
	require.Equal(t, CanonicalCodexCLITLSProfileName, ev["tls_profile"])
	require.Equal(t, "HTTP/1.1", ev["http_proto"])
	require.Equal(t, "", ev["alpn"], "WS hello carries no ALPN")
}

// A non-flagged account dials WSS through the stock crypto/tls path.
func TestOpenAIWSDialRecordsStdlibForNonFlaggedAccount(t *testing.T) {
	srv := newTLSIdentityWSServer(t)
	core, logs := observer.New(zap.InfoLevel)
	pool := &openAIWSConnPool{} // tlsProfileFor nil → no profile
	ctx := pool.withTLSProfile(logger.IntoContext(t.Context(), zap.New(core)), &Account{ID: 920002})
	require.Nil(t, openAIWSTLSProfileFromContext(ctx))

	// Stock dialer cannot trust the test cert: route via a crypto/tls proxy client.
	d := &coderOpenAIWSClientDialer{proxyClients: map[string]*openAIWSProxyClientEntry{}}
	_, ok, err := d.tkOpenAIWSTLSHTTPClient(ctx, "")
	require.NoError(t, err)
	require.False(t, ok)
	tkRecordOpenAIWSTLSIdentity(ctx, "", srv.Client(), &http.Response{
		Proto: "HTTP/1.1", TLS: &tls.ConnectionState{}, Request: &http.Request{URL: mustParseURL(wssURL(srv))},
	})

	events := logs.FilterMessage(UpstreamTLSIdentityMessage).All()
	require.Len(t, events, 1)
	ev := events[0].ContextMap()
	require.Equal(t, "wss", ev["transport"])
	require.Equal(t, "crypto/tls", ev["tls_stack"])
	require.Equal(t, "", ev["tls_profile"])
}

//go:build unit

package repository

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func antigravityTLSProfileForTest() *tlsfingerprint.Profile {
	return &tlsfingerprint.Profile{
		Name:          service.CanonicalAntigravityCLITLSProfileName,
		ALPNProtocols: []string{"h2", "http/1.1"},
	}
}

func TestGoogleCodeAssistHostDisablesHTTP2Ping(t *testing.T) {
	for _, host := range []string{
		"cloudcode-pa.googleapis.com",
		"daily-cloudcode-pa.googleapis.com",
		"DAILY-CLOUDCODE-PA.GOOGLEAPIS.COM",
		"cloudcode-pa.googleapis.com:443",
	} {
		require.True(t, googleCodeAssistDisablesHTTP2Ping(host), host)
	}
	for _, host := range []string{
		"",
		"generativelanguage.googleapis.com",
		"daily-cloudcode-pa.sandbox.googleapis.com",
		"notcloudcode-pa.googleapis.com",
		"cloudcode-pa.googleapis.com.evil.test",
		"evil.daily-cloudcode-pa.googleapis.com",
	} {
		require.False(t, googleCodeAssistDisablesHTTP2Ping(host), host)
	}
}

func TestCloudcodeHostUsesHTTP2WithoutPings(t *testing.T) {
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{Enabled: true})
	profiles := []*tlsfingerprint.Profile{
		antigravityTLSProfileForTest(),
		{Name: service.CanonicalKiroTLSProfileName, ALPNProtocols: []string{"h2", "http/1.1"}},
	}
	accountID := int64(8000)
	for _, host := range []string{"cloudcode-pa.googleapis.com", "daily-cloudcode-pa.googleapis.com"} {
		for _, profile := range profiles {
			accountID++
			entry, err := s.getClientEntryWithTLSForHost("", accountID, 1, profile, service.HTTPUpstreamProfileDefault, host, false, false)
			require.NoError(t, err)
			require.Equal(t, upstreamProtocolModeCloudcodeH2, entry.protocolMode)
			require.Contains(t, entry.poolKey, "|proto:"+upstreamProtocolModeCloudcodeH2)
			tr := entry.client.Transport.(*http.Transport)
			require.True(t, tr.ForceAttemptHTTP2, host)
			requireHTTP2Configured(t, tr, "Code Assist 主机必须保持 HTTP/2")
			require.Equal(t, time.Duration(0), tr.HTTP2.SendPingTimeout, host)
			require.Equal(t, time.Duration(0), tr.HTTP2.PingTimeout, host)
		}
	}
}

func TestCloudcodeHostTransportIsolatedFromOtherHosts(t *testing.T) {
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{})
	profile := antigravityTLSProfileForTest()
	const accountID = int64(529)
	quiet, err := s.getClientEntryWithTLSForHost("", accountID, 1, profile, service.HTTPUpstreamProfileDefault, "daily-cloudcode-pa.googleapis.com", false, false)
	require.NoError(t, err)
	other, err := s.getClientEntryWithTLSForHost("", accountID, 1, profile, service.HTTPUpstreamProfileDefault, "generativelanguage.googleapis.com", false, false)
	require.NoError(t, err)
	require.NotSame(t, quiet.client.Transport, other.client.Transport)
	require.Equal(t, time.Duration(0), quiet.client.Transport.(*http.Transport).HTTP2.SendPingTimeout)
	require.Equal(t, longStreamHTTP2ReadIdleTimeout, other.client.Transport.(*http.Transport).HTTP2.SendPingTimeout)
	require.Equal(t, longStreamHTTP2PingTimeout, other.client.Transport.(*http.Transport).HTTP2.PingTimeout)
	require.Empty(t, other.protocolMode)
}

func TestNonCodeAssistHostsKeepLongStreamPings(t *testing.T) {
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{})
	profile := antigravityTLSProfileForTest()
	accountID := int64(8100)
	for _, host := range []string{
		"",
		"daily-cloudcode-pa.sandbox.googleapis.com",
		"notcloudcode-pa.googleapis.com",
		"cloudcode-pa.googleapis.com.evil.test",
	} {
		accountID++
		entry, err := s.getClientEntryWithTLSForHost("", accountID, 1, profile, service.HTTPUpstreamProfileDefault, host, false, false)
		require.NoError(t, err, host)
		require.Empty(t, entry.protocolMode, host)
		require.NotContains(t, entry.poolKey, "|proto:", host)
		tr := entry.client.Transport.(*http.Transport)
		require.Equal(t, longStreamHTTP2ReadIdleTimeout, tr.HTTP2.SendPingTimeout, host)
		require.Equal(t, longStreamHTTP2PingTimeout, tr.HTTP2.PingTimeout, host)
	}
}

func TestCloudcodeHostDoesNotOverrideOpenAIPings(t *testing.T) {
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{Enabled: true})
	entry, err := s.getClientEntryWithTLSForHost("", 1, 1, codexTLSProfileForTest(), service.HTTPUpstreamProfileOpenAI, "daily-cloudcode-pa.googleapis.com", false, false)
	require.NoError(t, err)
	require.Equal(t, upstreamProtocolModeOpenAIH2, entry.protocolMode)
	tr := entry.client.Transport.(*http.Transport)
	require.Equal(t, openAIHTTP2ReadIdleTimeout, tr.HTTP2.SendPingTimeout)
	require.Equal(t, openAIHTTP2PingTimeout, tr.HTTP2.PingTimeout)
}

func TestBuildUpstreamTransport_CloudcodeH2_NegotiatesHTTP2WithoutPings(t *testing.T) {
	tr, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, upstreamProtocolModeCloudcodeH2)
	require.NoError(t, err)
	require.True(t, tr.ForceAttemptHTTP2)
	requireHTTP2Configured(t, tr, "cloudcode_h2 必须显式启用 HTTP/2")
	require.Equal(t, time.Duration(0), tr.HTTP2.SendPingTimeout)
	require.Equal(t, time.Duration(0), tr.HTTP2.PingTimeout)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	t.Cleanup(tr.CloseIdleConnections)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tr.TLSClientConfig.RootCAs = roots

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, resp.ProtoMajor, "cloudcode_h2 必须协商到 HTTP/2")
}

func TestCloudcodeFingerprintTransportNegotiatesHTTP2WithoutPings(t *testing.T) {
	transport, err := buildUpstreamTransportWithTLSFingerprintMode(poolSettings{}, nil, antigravityTLSProfileForTest(), upstreamProtocolModeCloudcodeH2)
	require.NoError(t, err)
	require.True(t, transport.ForceAttemptHTTP2)
	require.Equal(t, time.Duration(0), transport.HTTP2.SendPingTimeout)
	t.Cleanup(transport.CloseIdleConnections)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, 2, r.ProtoMajor)
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := (&tls.Dialer{Config: &tls.Config{
			InsecureSkipVerify: true, // test fixture only
			NextProtos:         []string{"h2", "http/1.1"},
		}}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return nonStdTLSConn{Conn: raw}, nil
	}

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, 2, resp.ProtoMajor)
	require.NoError(t, resp.Body.Close())
}

func TestDoWithTLSSelectsPingPolicyFromRequestHost(t *testing.T) {
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{})
	profile := antigravityTLSProfileForTest()
	const accountID = int64(529)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, rawURL := range []string{
		"https://daily-cloudcode-pa.googleapis.com:443/v1internal:streamGenerateContent?alt=sse",
		"https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse",
		"https://generativelanguage.googleapis.com/v1beta/models/gemini:streamGenerateContent",
	} {
		req, err := http.NewRequestWithContext(canceled, http.MethodPost, rawURL, nil)
		require.NoError(t, err)
		_, err = s.DoWithTLS(req, "", accountID, 1, profile)
		require.ErrorIs(t, err, context.Canceled, rawURL)
	}
	requireNoUpstreamInFlight(t, s)

	var quiet, other *upstreamClientEntry
	s.mu.RLock()
	for _, entry := range s.clients {
		switch entry.protocolMode {
		case upstreamProtocolModeCloudcodeH2:
			quiet = entry
		case "":
			other = entry
		}
	}
	s.mu.RUnlock()
	require.NotNil(t, quiet, "DoWithTLS 必须为 Code Assist 主机选中 cloudcode_h2")
	require.NotNil(t, other, "同一账号的其他主机必须仍走长流 PING 传输")
	require.NotSame(t, quiet.client.Transport, other.client.Transport)

	quietTr := quiet.client.Transport.(*http.Transport)
	otherTr := other.client.Transport.(*http.Transport)
	require.True(t, quietTr.ForceAttemptHTTP2)
	requireHTTP2Configured(t, quietTr, "DoWithTLS 必须为 Code Assist 保持 HTTP/2")
	require.Equal(t, time.Duration(0), quietTr.HTTP2.SendPingTimeout)
	require.Equal(t, time.Duration(0), quietTr.HTTP2.PingTimeout)
	require.Equal(t, longStreamHTTP2ReadIdleTimeout, otherTr.HTTP2.SendPingTimeout)
	require.Equal(t, longStreamHTTP2PingTimeout, otherTr.HTTP2.PingTimeout)
}

func TestCloudcodeHTTPSProxyKeepsHTTP2WithoutPings(t *testing.T) {
	proxyURL, err := url.Parse("https://proxy.test:8443")
	require.NoError(t, err)
	tr, err := buildUpstreamTransportWithTLSFingerprintMode(poolSettings{}, proxyURL, antigravityTLSProfileForTest(), upstreamProtocolModeCloudcodeH2)
	require.NoError(t, err)
	require.Nil(t, tr.DialTLSContext, "https proxy falls back to crypto/tls")
	require.True(t, tr.ForceAttemptHTTP2)
	requireHTTP2Configured(t, tr, "Code Assist HTTPS 代理回退仍须 HTTP/2")
	require.Equal(t, time.Duration(0), tr.HTTP2.SendPingTimeout)
}

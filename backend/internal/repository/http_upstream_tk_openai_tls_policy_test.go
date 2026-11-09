//go:build unit

package repository

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func codexTLSProfileForTest() *tlsfingerprint.Profile {
	return &tlsfingerprint.Profile{
		Name:          service.CanonicalCodexCLITLSProfileName,
		ALPNProtocols: []string{"h2", "http/1.1"},
		Extensions:    []uint16{0, 10, 13, 16, 43, 45, 51},
	}
}

func openAITLSPolicyService(t *testing.T, h2 config.GatewayOpenAIHTTP2Config) *httpUpstreamService {
	t.Helper()
	s, ok := NewHTTPUpstream(&config.Config{Gateway: config.GatewayConfig{OpenAIHTTP2: h2}}).(*httpUpstreamService)
	require.True(t, ok)
	return s
}

func requireHTTP1OnlyTransport(t *testing.T, tr *http.Transport) {
	t.Helper()
	require.False(t, tr.ForceAttemptHTTP2)
	require.NotNil(t, tr.TLSNextProto)
	require.Empty(t, tr.TLSNextProto)
	require.Nil(t, tr.HTTP2, "no h2 config on an HTTP/1.1-only transport")
}

func TestOpenAITLSFingerprintH2UsesOpenAIPingAndCodexWindows(t *testing.T) {
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{Enabled: true})
	entry, err := s.getClientEntryWithTLS("", 1, 1, codexTLSProfileForTest(), service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	require.Equal(t, upstreamProtocolModeOpenAIH2, entry.protocolMode)
	tr := entry.client.Transport.(*http.Transport)
	require.NotNil(t, tr.DialTLSContext)
	require.True(t, tr.ForceAttemptHTTP2)
	require.True(t, tr.Protocols.HTTP2())
	require.Equal(t, openAIHTTP2ReadIdleTimeout, tr.HTTP2.SendPingTimeout)
	require.Equal(t, openAIHTTP2PingTimeout, tr.HTTP2.PingTimeout)
	require.Equal(t, 15*time.Second, tr.HTTP2.SendPingTimeout)
	require.Zero(t, tr.HTTP2.MaxReceiveBufferPerStream, "stream window stays at Go's default")
	require.Zero(t, tr.HTTP2.MaxReceiveBufferPerConnection, "connection window stays at Go's default")
	require.Equal(t, codexH2MaxFrameSize, tr.HTTP2.MaxReadFrameSize)
	require.Contains(t, entry.poolKey, "|proto:"+upstreamProtocolModeOpenAIH2)
	require.NotContains(t, entry.poolKey, "h2win")
}

func TestOpenAITLSFingerprintCodexWindowsStayGoDefault(t *testing.T) {
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{Enabled: true})
	first, err := s.getClientEntryWithTLS("", 1, 1, codexTLSProfileForTest(), service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	second, err := s.getClientEntryWithTLS("", 1, 1, codexTLSProfileForTest(), service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	require.Same(t, first, second, "window size is not a pool-key input")
	require.Equal(t, first.poolKey, second.poolKey)
	require.NotContains(t, first.poolKey, "h2win")
	tr := first.client.Transport.(*http.Transport)
	require.Zero(t, tr.HTTP2.MaxReceiveBufferPerStream, "Go default stream window")
	require.Zero(t, tr.HTTP2.MaxReceiveBufferPerConnection, "Go default connection window")
	require.Equal(t, codexH2MaxFrameSize, tr.HTTP2.MaxReadFrameSize, "frame size stays fingerprint-aligned")
	require.Equal(t, int64(codexH2MaxResponseHeaderBytesForGo), tr.MaxResponseHeaderBytes)
	require.Equal(t, openAIHTTP2ReadIdleTimeout, tr.HTTP2.SendPingTimeout)
}

func TestOpenAITLSFingerprintH1FallbackOffersOnlyHTTP1(t *testing.T) {
	proxy := "http://proxy.test:8080"
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{Enabled: true, AllowProxyFallbackToHTTP1: true, FallbackErrorThreshold: 1})
	h2Entry, err := s.getClientEntryWithTLS(proxy, 1, 1, codexTLSProfileForTest(), service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	require.Equal(t, upstreamProtocolModeOpenAIH2, h2Entry.protocolMode)

	s.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxy,
		errors.New("stream error: stream ID 3; INTERNAL_ERROR; received from peer"))
	require.True(t, s.isOpenAIHTTP2FallbackActive(proxy))

	h1Entry, err := s.getClientEntryWithTLS(proxy, 1, 1, codexTLSProfileForTest(), service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	require.NotSame(t, h2Entry, h1Entry, "fallback must not reuse the h2 client")
	require.Equal(t, upstreamProtocolModeOpenAIH1Fallback, h1Entry.protocolMode)
	tr := h1Entry.client.Transport.(*http.Transport)
	require.NotNil(t, tr.DialTLSContext, "fingerprint dialer kept on fallback")
	requireHTTP1OnlyTransport(t, tr)

	base := codexTLSProfileForTest()
	derived := base.WithHTTP1OnlyALPN()
	require.Equal(t, []string{"http/1.1"}, derived.ALPNProtocols)
	require.Equal(t, base.Extensions, derived.Extensions, "ALPN extension stays in the hello")
	require.Equal(t, []string{"h2", "http/1.1"}, base.ALPNProtocols, "base profile not mutated")
}

func TestOpenAITLSFingerprintHTTP2DisabledUsesHTTP1(t *testing.T) {
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{Enabled: false})
	for _, proxy := range []string{"", "socks5://127.0.0.1:1080", "http://127.0.0.1:3128"} {
		entry, err := s.getClientEntryWithTLS(proxy, 1, 1, codexTLSProfileForTest(), service.HTTPUpstreamProfileOpenAI, false, false)
		require.NoError(t, err, proxy)
		require.Equal(t, upstreamProtocolModeOpenAIH1, entry.protocolMode, proxy)
		tr := entry.client.Transport.(*http.Transport)
		require.NotNil(t, tr.DialTLSContext, proxy)
		requireHTTP1OnlyTransport(t, tr)
	}
}

func TestOpenAITLSFingerprintHTTPSProxyKeepsOpenAIMode(t *testing.T) {
	pu, err := url.Parse("https://proxy.test:8443")
	require.NoError(t, err)
	tr, err := buildUpstreamTransportWithTLSFingerprintMode(poolSettings{}, pu, codexTLSProfileForTest(), upstreamProtocolModeOpenAIH2)
	require.NoError(t, err)
	require.Nil(t, tr.DialTLSContext, "https proxy falls back to crypto/tls")
	require.True(t, tr.ForceAttemptHTTP2)
	require.Equal(t, openAIHTTP2ReadIdleTimeout, tr.HTTP2.SendPingTimeout)
}

func TestNonOpenAITLSFingerprintTransportsUnchanged(t *testing.T) {
	// OpenAI HTTP/2 disabled must not leak into other platforms.
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{Enabled: false})
	profiles := []*tlsfingerprint.Profile{
		{Name: "tk_canonical_antigravity_cli", ALPNProtocols: []string{"h2", "http/1.1"}},
		{Name: service.CanonicalKiroTLSProfileName, ALPNProtocols: []string{"h2", "http/1.1"}},
		{Name: "tk_canonical_kiro_h1", ALPNProtocols: []string{"http/1.1"}},
	}
	accountID := int64(0)
	for _, upstreamProfile := range []service.HTTPUpstreamProfile{
		service.HTTPUpstreamProfileDefault, service.HTTPUpstreamProfileKiro, service.HTTPUpstreamProfileLongStream,
	} {
		for _, p := range profiles {
			// The TLS cache key is per account, not per profile: use a fresh
			// account per case so each case builds its own transport.
			accountID++
			entry, err := s.getClientEntryWithTLS("", accountID, 1, p, upstreamProfile, false, false)
			require.NoError(t, err)
			require.Empty(t, entry.protocolMode, "%s/%s", upstreamProfile, p.Name)
			require.NotContains(t, entry.poolKey, "|proto:")
			require.Contains(t, entry.poolKey, ":tls")
			tr := entry.client.Transport.(*http.Transport)
			legacy, err := buildUpstreamTransportWithTLSFingerprint(s.applyProfilePoolSettings(s.resolvePoolSettings(s.getIsolationMode(), 1), upstreamProfile), nil, p)
			require.NoError(t, err)
			require.Equal(t, legacy.ForceAttemptHTTP2, tr.ForceAttemptHTTP2)
			if !profileSupportsHTTP2(p) {
				requireHTTP1OnlyTransport(t, tr)
				continue
			}
			require.Equal(t, longStreamHTTP2ReadIdleTimeout, tr.HTTP2.SendPingTimeout)
			require.Equal(t, longStreamHTTP2PingTimeout, tr.HTTP2.PingTimeout)
			require.Zero(t, tr.HTTP2.MaxReceiveBufferPerConnection)
			require.Zero(t, tr.MaxResponseHeaderBytes)
		}
	}
}

func TestDoWithTLSRecordsOpenAIHTTP2FailureForHTTPProxy(t *testing.T) {
	proxy := "http://proxy.test:8080"
	s := openAITLSPolicyService(t, config.GatewayOpenAIHTTP2Config{Enabled: true, AllowProxyFallbackToHTTP1: true, FallbackErrorThreshold: 2})
	profile := codexTLSProfileForTest()
	entry, err := s.getClientEntryWithTLS(proxy, 1, 1, profile, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(t, err)
	reset := errors.New("stream error: stream ID 5; INTERNAL_ERROR; received from peer")
	entry.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, reset })}

	ctx := service.WithHTTPUpstreamProfile(t.Context(), service.HTTPUpstreamProfileOpenAI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = s.DoWithTLS(req, proxy, 1, 1, profile)
		require.ErrorIs(t, err, reset)
	}
	require.True(t, s.isOpenAIHTTP2FallbackActive(proxy), "DoWithTLS must feed the HTTP-proxy H1 fallback")
	requireNoUpstreamInFlight(t, s)
}

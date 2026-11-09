//go:build unit

package repository

import (
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexCLIProfileTransportKeepsHTTP2AndCodexSettings(t *testing.T) {
	t.Parallel()
	codex := &tlsfingerprint.Profile{Name: service.CanonicalCodexCLITLSProfileName, ALPNProtocols: []string{"h2", "http/1.1"}}
	for _, proxy := range []string{"", "http://u:p@127.0.0.1:3128", "socks5://127.0.0.1:1080"} {
		var pu *url.URL
		if proxy != "" {
			pu, _ = url.Parse(proxy)
		}
		tr, err := buildUpstreamTransportWithTLSFingerprint(poolSettings{}, pu, codex)
		require.NoError(t, err, proxy)
		require.NotNil(t, tr.DialTLSContext, "uTLS dialer must be used for %q", proxy)
		require.True(t, tr.ForceAttemptHTTP2, "h2 must not silently downgrade (%q)", proxy)
		require.True(t, tr.Protocols.HTTP2())
		require.NotNil(t, tr.HTTP2)
		require.Zero(t, tr.HTTP2.MaxReceiveBufferPerStream, "stream window stays at Go's default")
		require.Zero(t, tr.HTTP2.MaxReceiveBufferPerConnection, "connection window stays at Go's default")
		require.Equal(t, codexH2MaxFrameSize, tr.HTTP2.MaxReadFrameSize)
		require.Equal(t, int64(codexH2MaxResponseHeaderBytesForGo), tr.MaxResponseHeaderBytes)
		require.NotZero(t, tr.HTTP2.SendPingTimeout, "keepalive PING still enabled")
	}

	other, err := buildUpstreamTransportWithTLSFingerprint(poolSettings{}, nil, &tlsfingerprint.Profile{Name: "tk_canonical_antigravity_cli", ALPNProtocols: []string{"h2"}})
	require.NoError(t, err)
	require.Zero(t, other.HTTP2.MaxReceiveBufferPerConnection, "other profiles keep Go defaults")
	require.Zero(t, other.MaxResponseHeaderBytes)
}

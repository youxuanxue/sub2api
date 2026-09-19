package repository

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// nonStdTLSConn mimics utls.UConn: exposes ConnectionState but is not *tls.Conn.
type nonStdTLSConn struct{ net.Conn }

type connectionStater interface {
	ConnectionState() tls.ConnectionState
}

func (c nonStdTLSConn) ConnectionState() tls.ConnectionState {
	if cs, ok := c.Conn.(connectionStater); ok {
		return cs.ConnectionState()
	}
	return tls.ConnectionState{}
}

func TestTLSFingerprintHTTP2RoundTripAcceptsNonStdTLSConn(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, 2, r.ProtoMajor, "server must see HTTP/2")
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	transport, err := buildUpstreamTransportWithTLSFingerprint(poolSettings{}, nil, &tlsfingerprint.Profile{
		Name:          "antigravity-cli",
		ALPNProtocols: []string{"h2", "http/1.1"},
	})
	require.NoError(t, err)
	t.Cleanup(transport.CloseIdleConnections)

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
	require.NoError(t, err, "non-*tls.Conn dial must still negotiate HTTP/2")
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, 2, resp.ProtoMajor)
	require.NoError(t, resp.Body.Close())
}

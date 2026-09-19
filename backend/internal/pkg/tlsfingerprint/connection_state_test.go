package tlsfingerprint

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestTLSFingerprintConnExposesHTTP2ALPNToNetHTTP(t *testing.T) {
	client, peer := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = peer.Close()
	})

	conn := newTLSFingerprintConn(client, utls.ConnectionState{
		Version:            utls.VersionTLS13,
		HandshakeComplete:  true,
		NegotiatedProtocol: "h2",
	})
	stateConn, ok := conn.(interface{ ConnectionState() tls.ConnectionState })
	require.True(t, ok, "uTLS connections must expose crypto/tls.ConnectionState to net/http")

	state := stateConn.ConnectionState()
	require.Equal(t, uint16(utls.VersionTLS13), state.Version)
	require.True(t, state.HandshakeComplete)
	require.Equal(t, "h2", state.NegotiatedProtocol)
}

func TestTLSFingerprintConnMakesNetHTTPUseHTTP2(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, 2, r.ProtoMajor)
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	transport := &http.Transport{ForceAttemptHTTP2: true}
	_, err := http2.ConfigureTransports(transport)
	require.NoError(t, err)
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		tlsConn, err := (&tls.Dialer{Config: &tls.Config{
			InsecureSkipVerify: true, // test fixture only
			NextProtos:         []string{"h2", "http/1.1"},
		}}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		stateConn, ok := tlsConn.(interface{ ConnectionState() tls.ConnectionState })
		if !ok {
			return nil, errors.New("TLS fixture connection does not expose connection state")
		}
		state := stateConn.ConnectionState()
		return newTLSFingerprintConn(tlsConn, utls.ConnectionState{
			Version:            state.Version,
			HandshakeComplete:  state.HandshakeComplete,
			CipherSuite:        state.CipherSuite,
			NegotiatedProtocol: state.NegotiatedProtocol,
			ServerName:         state.ServerName,
		}), nil
	}
	t.Cleanup(transport.CloseIdleConnections)

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	response, err := transport.RoundTrip(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, response.StatusCode)
	require.Equal(t, 2, response.ProtoMajor)
	require.NoError(t, response.Body.Close())
}

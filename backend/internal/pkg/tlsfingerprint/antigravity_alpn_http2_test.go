package tlsfingerprint

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/require"
)

func antigravityCLIProfile() *Profile {
	return &Profile{
		Name:                "tk_canonical_antigravity_cli",
		EnableGREASE:        false,
		ShuffleExtensions:   false,
		CipherSuites:        []uint16{49195, 49199, 49196, 49200, 52393, 52392, 49161, 49171, 49162, 49172, 4865, 4866, 4867},
		Curves:              []uint16{4588, 4587, 4589, 29, 23, 24, 25},
		PointFormats:        []uint16{0},
		SignatureAlgorithms: []uint16{2308, 2309, 2310, 2052, 1027, 2055, 2053, 2054, 1025, 1281, 1537, 1283, 1539},
		ALPNProtocols:       []string{"h2", "http/1.1"},
		SupportedVersions:   []uint16{772, 771},
		KeyShareGroups:      []uint16{4588, 29},
		Extensions:          []uint16{0, 11, 65281, 23, 18, 5, 10, 13, 50, 16, 43, 51},
	}
}

func TestAntigravityCLIProfileNegotiatesH2AgainstHTTP2Server(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	hostport := server.Listener.Addr().String()
	raw, err := net.Dial("tcp", hostport)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	host, _, err := net.SplitHostPort(hostport)
	require.NoError(t, err)
	spec := buildClientHelloSpecFromProfile(antigravityCLIProfile())
	uconn := utls.UClient(raw, &utls.Config{ServerName: host, InsecureSkipVerify: true}, utls.HelloCustom)
	require.NoError(t, uconn.ApplyPreset(spec))
	require.NoError(t, uconn.HandshakeContext(context.Background()))
	state := uconn.ConnectionState()
	t.Logf("alpn=%q version=%x", state.NegotiatedProtocol, state.Version)
	require.Equal(t, "h2", state.NegotiatedProtocol)
}

func TestAntigravityCLIProfileHTTPRoundTripUsesHTTP2(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, 2, r.ProtoMajor)
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	profile := antigravityCLIProfile()
	transport := &http.Transport{
		ForceAttemptHTTP2: true,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Mirror production dialer but skip verify for fixture cert.
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}
			raw, err := net.Dial(network, addr)
			if err != nil {
				return nil, err
			}
			spec := buildClientHelloSpecFromProfile(profile)
			uconn := utls.UClient(raw, &utls.Config{ServerName: host, InsecureSkipVerify: true}, utls.HelloCustom)
			if err := uconn.ApplyPreset(spec); err != nil {
				_ = raw.Close()
				return nil, err
			}
			if err := uconn.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return asNetHTTPConn(uconn, uconn.ConnectionState()), nil
		},
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	transport.Protocols = protocols

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, 2, resp.ProtoMajor)
	require.NoError(t, resp.Body.Close())
}

func TestAntigravityIDECloudcodeProfileOmitsALPN(t *testing.T) {
	profile := NewAntigravityIDECloudcodeProfile()
	spec := buildClientHelloSpecFromProfile(profile)
	if containsExtension(spec.Extensions, 16) {
		t.Fatal("official IDE cloudcode profile must omit ALPN extension")
	}
	if len(profile.ALPNProtocols) != 0 {
		t.Fatalf("official IDE cloudcode profile unexpectedly advertises ALPN: %v", profile.ALPNProtocols)
	}
	if profile.Name != AntigravityIDECloudcodeProfileName {
		t.Fatalf("unexpected profile name %q", profile.Name)
	}
	if got, want := profile.Extensions, []uint16{0, 11, 65281, 23, 18, 5, 10, 13, 50, 43, 51}; !reflect.DeepEqual(got, want) {
		t.Fatalf("official IDE cloudcode extension order drifted: got %v want %v", got, want)
	}
	if got, want := profile.CipherSuites, []uint16{49195, 49199, 49196, 49200, 52393, 52392, 49161, 49171, 49162, 49172, 4865, 4866, 4867}; !reflect.DeepEqual(got, want) {
		t.Fatalf("official IDE cloudcode cipher order drifted: got %v want %v", got, want)
	}
}

func TestStdTLSConnectionStatePreservesNegotiationMetadata(t *testing.T) {
	state := stdTLSConnectionState(utls.ConnectionState{
		Version:            utls.VersionTLS13,
		HandshakeComplete:  true,
		NegotiatedProtocol: "h2",
		ECHAccepted:        true,
	})

	require.Equal(t, uint16(utls.VersionTLS13), state.Version)
	require.True(t, state.HandshakeComplete)
	require.Equal(t, "h2", state.NegotiatedProtocol)
	require.True(t, state.ECHAccepted)
}

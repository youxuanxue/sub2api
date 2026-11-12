//go:build unit

package tlsfingerprint

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/require"
)

// codexCLIProfile loads deploy/aws/stage0/tk_canonical_codex_cli.json (the
// committed capture of real codex-cli 0.167.2) so the replay test cannot drift
// from the owner file.
func codexCLIProfile(t *testing.T) *Profile {
	t.Helper()
	raw, err := os.ReadFile("../../../../deploy/aws/stage0/tk_canonical_codex_cli.json")
	require.NoError(t, err)
	var j struct {
		Name                string   `json:"name"`
		EnableGREASE        bool     `json:"enable_grease"`
		ShuffleExtensions   bool     `json:"shuffle_extensions"`
		CipherSuites        []uint16 `json:"cipher_suites"`
		Curves              []uint16 `json:"curves"`
		PointFormats        []uint16 `json:"point_formats"`
		SignatureAlgorithms []uint16 `json:"signature_algorithms"`
		ALPNProtocols       []string `json:"alpn_protocols"`
		SupportedVersions   []uint16 `json:"supported_versions"`
		KeyShareGroups      []uint16 `json:"key_share_groups"`
		PSKModes            []uint16 `json:"psk_modes"`
		Extensions          []uint16 `json:"extensions"`
	}
	require.NoError(t, json.Unmarshal(raw, &j))
	return &Profile{
		Name: j.Name, EnableGREASE: j.EnableGREASE, ShuffleExtensions: j.ShuffleExtensions,
		CipherSuites: j.CipherSuites, Curves: j.Curves, PointFormats: j.PointFormats,
		SignatureAlgorithms: j.SignatureAlgorithms, ALPNProtocols: j.ALPNProtocols,
		SupportedVersions: j.SupportedVersions, KeyShareGroups: j.KeyShareGroups,
		PSKModes: j.PSKModes, Extensions: j.Extensions,
	}
}

type parsedHello struct {
	ciphers, exts, groups, sigalgs, versions, keyShares, psk []uint16
	alpn                                                     []string
	sessionIDLen                                             int
}

func parseClientHelloRecord(t *testing.T, rec []byte) parsedHello {
	t.Helper()
	require.Greater(t, len(rec), 9)
	require.Equal(t, byte(22), rec[0])
	b := rec[5+4+2+32:]
	var h parsedHello
	h.sessionIDLen = int(b[0])
	b = b[1+int(b[0]):]
	n := int(binary.BigEndian.Uint16(b))
	for i := 0; i < n; i += 2 {
		h.ciphers = append(h.ciphers, binary.BigEndian.Uint16(b[2+i:]))
	}
	b = b[2+n:]
	b = b[1+int(b[0]):]
	e := b[2 : 2+int(binary.BigEndian.Uint16(b))]
	u16s := func(x []byte) (out []uint16) {
		for i := 0; i+1 < len(x); i += 2 {
			out = append(out, binary.BigEndian.Uint16(x[i:]))
		}
		return
	}
	for len(e) >= 4 {
		id := binary.BigEndian.Uint16(e)
		l := int(binary.BigEndian.Uint16(e[2:]))
		d := e[4 : 4+l]
		e = e[4+l:]
		h.exts = append(h.exts, id)
		switch id {
		case 10:
			h.groups = u16s(d[2:])
		case 13:
			h.sigalgs = u16s(d[2:])
		case 16:
			for x := d[2:]; len(x) > 0; x = x[1+int(x[0]):] {
				h.alpn = append(h.alpn, string(x[1:1+int(x[0])]))
			}
		case 43:
			h.versions = u16s(d[1:])
		case 45:
			for _, m := range d[1:] {
				h.psk = append(h.psk, uint16(m))
			}
		case 51:
			for x := d[2:]; len(x) >= 4; x = x[4+int(binary.BigEndian.Uint16(x[2:])):] {
				h.keyShares = append(h.keyShares, binary.BigEndian.Uint16(x))
			}
		}
	}
	return h
}

// captureHello dials an in-memory pipe with the production handshake path and
// returns the raw first TLS record (the ClientHello).
func captureHello(t *testing.T, p *Profile) parsedHello {
	t.Helper()
	client, server := net.Pipe()
	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 5)
		_, _ = ioReadFull(server, buf)
		body := make([]byte, int(binary.BigEndian.Uint16(buf[3:])))
		_, _ = ioReadFull(server, body)
		got <- append(buf, body...)
		_ = server.Close()
	}()
	_, _ = performTLSHandshake(context.Background(), client, p, "chatgpt.com:443")
	return parseClientHelloRecord(t, <-got)
}

func ioReadFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func TestCodexCLIProfileClientHelloMatchesCapture(t *testing.T) {
	t.Parallel()
	p := codexCLIProfile(t)
	require.Equal(t, "tk_canonical_codex_cli", p.Name)
	orders := map[string]bool{}
	for i := 0; i < 8; i++ {
		h := captureHello(t, p)
		require.Equal(t, p.CipherSuites, h.ciphers, "cipher order is part of JA3/JA4")
		require.ElementsMatch(t, p.Extensions, h.exts, "same extension set, no GREASE/padding")
		require.Equal(t, p.Curves, h.groups)
		require.Equal(t, p.SignatureAlgorithms, h.sigalgs)
		require.Equal(t, []string{"h2", "http/1.1"}, h.alpn)
		require.Equal(t, p.SupportedVersions, h.versions)
		require.Equal(t, []uint16{uint16(utls.X25519MLKEM768), uint16(utls.X25519)}, h.keyShares)
		require.Equal(t, []uint16{1}, h.psk)
		require.Equal(t, 32, h.sessionIDLen)
		b, _ := json.Marshal(h.exts)
		orders[string(b)] = true
	}
	require.Greater(t, len(orders), 1, "rustls-style per-connection extension shuffle expected")
}

func TestCodexCLIProfileNegotiatesH2ThroughProductionDialer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	raw, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	spec := buildClientHelloSpecFromProfile(codexCLIProfile(t))
	u := utls.UClient(raw, &utls.Config{ServerName: "127.0.0.1", InsecureSkipVerify: true}, utls.HelloCustom)
	require.NoError(t, u.ApplyPreset(spec))
	require.NoError(t, u.HandshakeContext(context.Background()))
	require.Equal(t, "h2", u.ConnectionState().NegotiatedProtocol)
	require.Equal(t, uint16(tls.VersionTLS13), u.ConnectionState().Version)
	conn := asNetHTTPConn(u, u.ConnectionState())
	st, ok := conn.(interface{ ConnectionState() tls.ConnectionState })
	require.True(t, ok)
	require.Equal(t, "h2", st.ConnectionState().NegotiatedProtocol, "net/http must see h2 (no silent HTTP/1.1 downgrade)")
	_ = conn.Close()
}

func TestCodexCLIWSProfileWithoutALPNStaysHTTP1(t *testing.T) {
	t.Parallel()
	p := codexCLIProfile(t)
	ws := *p
	ws.ALPNProtocols = nil
	ws.Extensions = slices.DeleteFunc(slices.Clone(p.Extensions), func(id uint16) bool { return id == 16 })
	h := captureHello(t, &ws)
	require.NotContains(t, h.exts, uint16(16), "real codex WS hello has no ALPN (JA4 ...00_)")
	require.Nil(t, h.alpn)
}

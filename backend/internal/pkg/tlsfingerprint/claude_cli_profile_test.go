//go:build unit

package tlsfingerprint

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeCLIProfileClientHelloMatchesCapture(t *testing.T) {
	t.Parallel()
	p := capturedCLIProfile(t, "tk_canonical_cc_oauth.json")
	raw, err := os.ReadFile("../../../../deploy/stage0/tk_canonical_cc_oauth.json")
	require.NoError(t, err)
	var captured struct {
		Observed struct {
			JA3Raw  string `json:"ja3_raw"`
			JA3Hash string `json:"ja3_hash"`
		} `json:"observed"`
	}
	require.NoError(t, json.Unmarshal(raw, &captured))
	join := func(values []uint16) string {
		parts := make([]string, len(values))
		for i, v := range values {
			parts[i] = strconv.Itoa(int(v))
		}
		return strings.Join(parts, "-")
	}
	for i := 0; i < 3; i++ {
		h := captureHello(t, p)
		require.Equal(t, p.CipherSuites, h.ciphers)
		require.Equal(t, p.Extensions, h.exts, "Node extension order must remain stable")
		require.Equal(t, p.Curves, h.groups)
		require.Equal(t, p.PointFormats, h.points)
		require.Equal(t, p.SignatureAlgorithms, h.sigalgs)
		require.Equal(t, p.ALPNProtocols, h.alpn)
		require.Equal(t, p.SupportedVersions, h.versions)
		require.Equal(t, p.KeyShareGroups, h.keyShares)
		require.Equal(t, p.PSKModes, h.psk)
		ja3 := fmt.Sprintf("771,%s,%s,%s,%s", join(h.ciphers), join(h.exts), join(h.groups), join(h.points))
		require.Equal(t, captured.Observed.JA3Raw, ja3)
		digest := md5.Sum([]byte(ja3))
		require.Equal(t, captured.Observed.JA3Hash, hex.EncodeToString(digest[:]))
	}
}

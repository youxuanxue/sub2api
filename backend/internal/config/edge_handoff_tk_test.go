//go:build unit

package config

import (
	"encoding/base64"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestEdgeHandoffConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	disabled, err := LoadEdgeHandoffConfig(path)
	require.NoError(t, err)
	require.Nil(t, disabled.Receiver)
	cfg := EdgeHandoffConfig{Version: 1, Issuer: "https://prod.example", Signers: map[string]EdgeHandoffSigner{"e1": {Origin: "https://edge.example", KeyID: "k1", Seed: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}}}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	loaded, err := LoadEdgeHandoffConfig(path)
	require.NoError(t, err)
	require.Equal(t, cfg.Issuer, loaded.Issuer)
	require.NoError(t, os.Chmod(path, 0644))
	_, err = LoadEdgeHandoffConfig(path)
	require.ErrorContains(t, err, "private file permissions")
	require.NoError(t, os.Chmod(path, 0600))
	require.NoError(t, os.WriteFile(path, append(raw, []byte(` {}`)...), 0600))
	_, err = LoadEdgeHandoffConfig(path)
	require.Error(t, err)
}
func TestEdgeHandoffOrigin(t *testing.T) {
	for _, origin := range []string{"https://prod.example", "http://127.0.0.1:4311", "http://[::1]:4311"} {
		require.True(t, EdgeHandoffOrigin(origin), origin)
	}
	for _, origin := range []string{"https://prod.example/", "https://user:secret@prod.example", "https://prod.example?next=x", "https://prod.example#token", "http://prod.example", "javascript:alert(1)", "https://prod.example?", "https://prod.example/#"} {
		require.False(t, EdgeHandoffOrigin(origin), origin)
	}
}

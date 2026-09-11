package logredact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactSecretsInContentAndToolResults(t *testing.T) {
	secrets := []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\nSYNTHETIC-KEY-MATERIAL\n-----END OPENSSH PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----\nSYNTHETIC-TRUNCATED-KEY",
		"glpat-" + strings.Repeat("s", 24),
		"LTAI" + strings.Repeat("s", 20),
		"AKIA" + strings.Repeat("A", 16),
		"sk-" + strings.Repeat("s", 32),
		"PrivateKey = SYNTHETIC-WIREGUARD-KEY",
		"Bearer SYNTHETIC-BEARER-TOKEN",
	}
	for _, secret := range secrets {
		input, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "tool", "content": "before " + secret + " after"}}})
		require.NoError(t, err)
		out := RedactJSON(input)
		require.True(t, json.Valid([]byte(out)))
		var decoded struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &decoded))
		require.Len(t, decoded.Messages, 1)
		content := decoded.Messages[0].Content
		require.NotContains(t, content, secret)
		require.NotContains(t, content, "SYNTHETIC-KEY-MATERIAL")
		require.NotContains(t, content, "SYNTHETIC-WIREGUARD-KEY")
		require.NotContains(t, content, "SYNTHETIC-BEARER-TOKEN")
	}
}

func TestRedactJSONStringPreservesOrdinaryContentAndTerminates(t *testing.T) {
	for _, value := range []string{" hello\nworld ", `"quoted"`, `{"message":"hello"}`, "123", "true"} {
		input, err := json.Marshal(value)
		require.NoError(t, err)
		require.JSONEq(t, string(input), RedactJSON(input))
	}
	require.NotContains(t, RedactJSON([]byte(`{"content":"special=synthetic"}`), "special"), "synthetic")
}

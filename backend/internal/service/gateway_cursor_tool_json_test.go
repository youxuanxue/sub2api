//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCursorToolJSONDropsAnthropicStartPlaceholder(t *testing.T) {
	args := json.RawMessage(`{}`)
	for _, part := range []string{`{"path":`, `"fixture.txt"}`} {
		args = appendAnthropicToolJSON(args, part)
	}
	var parsed map[string]string
	require.NoError(t, json.Unmarshal(args, &parsed))
	require.Equal(t, "fixture.txt", parsed["path"])
	require.JSONEq(t, `{}`, string(appendAnthropicToolJSON(json.RawMessage(`{}`), "")))
}

//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
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

func TestAnthropicBufferedAssemblyIgnoresInvalidContentIndex(t *testing.T) {
	for _, index := range []int{-1, 1} {
		assembly := tkAnthropicBufferedAssembly{FinalResp: &apicompat.AnthropicResponse{
			Content: []apicompat.AnthropicContentBlock{{Type: "text", Text: "original"}},
		}}
		var event apicompat.AnthropicStreamEvent
		require.NoError(t, json.Unmarshal([]byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"corrupt"}}`), &event))
		event.Index = &index
		require.NotPanics(t, func() { assembly.applyEvent(&event) })
		require.Equal(t, "original", assembly.FinalResp.Content[0].Text)
	}
}

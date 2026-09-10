//go:build unit

package kiro

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseEventStream_PreservesIncrementalText(t *testing.T) {
	// Kiro CLI 2.21.1 appends these same frames verbatim in both agent engines.
	cases := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"overlapping word", []string{"scre", "en"}, "screen"},
		{"identical deltas", []string{"foo", "foo"}, "foofoo"},
		{"prefix delta", []string{"abc", "ab"}, "abcab"},
		{"growing delta is not a snapshot", []string{"a", "ab", "abc"}, "aababc"},
		{"repeated digits", []string{"sha256=", "1111", "1111", "2222", "2222"}, "sha256=1111111122222222"},
		{"empty delta", []string{"same", "", "same"}, "samesame"},
		{"unicode", []string{"\u5b8c\u6210", "\u6210\u529f"}, "\u5b8c\u6210\u6210\u529f"},
	}
	for _, channel := range []string{"text", "reasoningText", "reasoningTextObject", "reasoningContentEvent"} {
		for _, tc := range cases {
			t.Run(channel+"/"+tc.name, func(t *testing.T) {
				var stream []byte
				for _, chunk := range tc.chunks {
					kind := "assistantResponseEvent"
					event := map[string]any{"content": chunk}
					switch channel {
					case "reasoningText":
						event = map[string]any{"reasoningText": chunk}
					case "reasoningTextObject":
						event = map[string]any{"reasoningText": map[string]any{"text": chunk}}
					case "reasoningContentEvent":
						kind = channel
						event = map[string]any{"text": chunk, "signature": "test-signature"}
					}
					payload, err := json.Marshal(event)
					require.NoError(t, err)
					stream = append(stream, buildEventStreamMessage(kind, payload)...)
				}
				stream = append(stream, buildEventStreamMessage("metadataEvent", []byte(`{"stopReason":"END_TURN"}`))...)
				var text, reasoning strings.Builder
				var signature string
				err := parseEventStream(bytes.NewReader(stream), &KiroStreamCallback{
					OnText: func(s string, thinking bool) {
						require.False(t, thinking)
						text.WriteString(s)
					},
					OnReasoningContent: func(s, sig string) {
						reasoning.WriteString(s)
						if sig != "" {
							signature = sig
						}
					},
				})
				require.NoError(t, err)
				if channel == "text" {
					require.Equal(t, tc.want, text.String())
					require.Empty(t, reasoning.String())
				} else {
					require.Equal(t, tc.want, reasoning.String())
					require.Empty(t, text.String())
				}
				if channel == "reasoningContentEvent" {
					require.Equal(t, "test-signature", signature)
				}
			})
		}
	}
}

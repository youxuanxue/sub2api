package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToChatCompletionsRequest_RejectsVideoParts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   string
		wantMsg string
	}{
		{
			name:    "input_video",
			input:   `[{"role":"user","content":[{"type":"input_text","text":"color?"},{"type":"input_video","video_url":"https://example.com/a.mp4"}]}]`,
			wantMsg: `responses content part "input_video" is not supported`,
		},
		{
			name:    "video",
			input:   `[{"type":"message","role":"user","content":[{"type":"video","source":{"type":"url","url":"https://example.com/a.mp4"}}]}]`,
			wantMsg: `unsupported responses content part "video"`,
		},
		{
			name:    "top_level_input_video",
			input:   `[{"type":"input_video","video_url":"https://example.com/a.mp4"}]`,
			wantMsg: `responses content part "input_video" is not supported`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResponsesToChatCompletionsRequest(&ResponsesRequest{
				Model: "kimi-k3.1-pro",
				Input: json.RawMessage(tc.input),
			})
			require.Error(t, err)
			require.Equal(t, tc.wantMsg, err.Error())
		})
	}
}

func TestResponsesToChatCompletionsRequest_KeepsImageParts(t *testing.T) {
	t.Parallel()
	out, err := ResponsesToChatCompletionsRequest(&ResponsesRequest{
		Model: "kimi-k3.1-pro",
		Input: json.RawMessage(`[{"role":"user","content":[{"type":"input_text","text":"what"},{"type":"input_image","image_url":"https://example.com/a.png"}]}]`),
	})
	require.NoError(t, err)
	require.Len(t, out.Messages, 1)
	require.Contains(t, string(out.Messages[0].Content), "image_url")
}

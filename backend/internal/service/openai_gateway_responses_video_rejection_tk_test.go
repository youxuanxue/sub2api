package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesUnsupportedVideoPartMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "text_only",
			body: `{"model":"kimi-k3.1-pro","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			want: "",
		},
		{
			name: "string_input",
			body: `{"model":"kimi-k3.1-pro","input":"hello"}`,
			want: "",
		},
		{
			name: "input_video_part",
			body: `{"model":"kimi-k3.1-pro","input":[{"role":"user","content":[{"type":"input_text","text":"color?"},{"type":"input_video","video_url":"https://example.com/a.mp4"}]}]}`,
			want: responsesInputVideoUnsupportedMessage,
		},
		{
			name: "video_part",
			body: `{"model":"kimi-k3.1-pro","input":[{"type":"message","role":"user","content":[{"type":"video","source":{"type":"url","url":"https://example.com/a.mp4"}}]}]}`,
			want: responsesVideoUnsupportedMessage,
		},
		{
			name: "video_url_part",
			body: `{"model":"kimi-k3.1-pro","input":[{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}}]}]}`,
			want: responsesVideoURLUnsupportedMessage,
		},
		{
			name: "top_level_input_video_item",
			body: `{"model":"kimi-k3.1-pro","input":[{"type":"input_video","video_url":"https://example.com/a.mp4"}]}`,
			want: responsesInputVideoUnsupportedMessage,
		},
		{
			name: "image_still_allowed",
			body: `{"model":"kimi-k3.1-pro","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`,
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, ResponsesUnsupportedVideoPartMessage([]byte(tc.body)))
		})
	}
}

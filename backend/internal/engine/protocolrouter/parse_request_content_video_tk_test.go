//go:build unit

package protocolrouter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCanonicalRequestClassifiesVideoURL(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}}]}]}`)
	req, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "kimi-k3", false, body)
	require.NoError(t, err)
	kinds := req.Profile().ContentKinds
	require.NotZero(t, kinds&ContentText)
	require.NotZero(t, kinds&ContentVideo)
	require.Zero(t, kinds&ContentImage)

	inputVideo := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"input_video","video_url":"https://example.com/b.mp4"}]}]}`)
	req, err = ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "kimi-k3", false, inputVideo)
	require.NoError(t, err)
	require.NotZero(t, req.Profile().ContentKinds&ContentVideo)

	textOnly := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`)
	req, err = ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "kimi-k3", false, textOnly)
	require.NoError(t, err)
	require.Zero(t, req.Profile().ContentKinds&ContentVideo)
}

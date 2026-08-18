package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIPassthroughOAuthBody_RemovesUnsupportedUser(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","user":"user_123","metadata":{"user_id":"user_123"},"prompt_cache_retention":"24h","safety_identifier":"sid","stream_options":{"include_usage":true}}`)

	normalized, changed, err := normalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)
	for _, field := range openAIChatGPTInternalUnsupportedFields {
		require.False(t, gjson.GetBytes(normalized, field).Exists(), "%s should be stripped", field)
	}
	require.True(t, gjson.GetBytes(normalized, "stream").Bool())
	require.False(t, gjson.GetBytes(normalized, "store").Bool())
}

func TestNormalizeOpenAIPassthroughOAuthBody_CompactRemovesUnsupportedUser(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","user":"user_123","metadata":{"user_id":"user_123"},"stream":true,"store":true}`)

	normalized, changed, err := normalizeOpenAIPassthroughOAuthBody(body, true)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(normalized, "user").Exists())
	require.False(t, gjson.GetBytes(normalized, "metadata").Exists())
	require.False(t, gjson.GetBytes(normalized, "stream").Exists())
	require.False(t, gjson.GetBytes(normalized, "store").Exists())
}

func TestNormalizeOpenAIPassthroughOAuthBody_StringInputWrappedAsArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello world"}`)

	normalized, changed, err := normalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)

	input := gjson.GetBytes(normalized, "input")
	require.True(t, input.IsArray(), "string input should be converted to array")
	items := input.Array()
	require.Len(t, items, 1)
	require.Equal(t, "message", items[0].Get("type").String())
	require.Equal(t, "user", items[0].Get("role").String())
	require.Equal(t, "hello world", items[0].Get("content").String())
}

func TestNormalizeOpenAIPassthroughOAuthBody_EmptyStringInputWrappedAsEmptyArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"  "}`)

	normalized, changed, err := normalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)

	input := gjson.GetBytes(normalized, "input")
	require.True(t, input.IsArray())
	require.Len(t, input.Array(), 0, "whitespace-only input should become empty array")
}

func TestNormalizeOpenAIPassthroughOAuthBody_ObjectInputWrappedAsArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":{"type":"message","role":"user","content":"hi"}}`)

	normalized, changed, err := normalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)

	input := gjson.GetBytes(normalized, "input")
	require.True(t, input.IsArray(), "object input should be wrapped in array")
	items := input.Array()
	require.Len(t, items, 1)
	require.Equal(t, "message", items[0].Get("type").String())
}

func TestNormalizeOpenAIPassthroughOAuthBody_ArrayInputUnchanged(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":"hi"}]}`)

	normalized, _, err := normalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)

	input := gjson.GetBytes(normalized, "input")
	require.True(t, input.IsArray())
	require.Len(t, input.Array(), 1)
	require.Equal(t, "message", input.Array()[0].Get("type").String())
}

func TestNormalizeOpenAIPassthroughOAuthBody_SetsReasoningSummaryAuto(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4-mini","input":[{"type":"message","role":"user","content":"hi"}]}`)

	normalized, changed, err := normalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "auto", gjson.GetBytes(normalized, "reasoning.summary").String())
	require.Equal(t, `["reasoning.encrypted_content"]`, gjson.GetBytes(normalized, "include").Raw)

	already := []byte(`{"model":"gpt-5.4-mini","input":[{"type":"message","role":"user","content":"hi"}],"reasoning":{"effort":"medium","summary":"concise"},"include":["foo"]}`)
	normalized, _, err = normalizeOpenAIPassthroughOAuthBody(already, false)
	require.NoError(t, err)
	require.Equal(t, "auto", gjson.GetBytes(normalized, "reasoning.summary").String())
	require.Equal(t, "medium", gjson.GetBytes(normalized, "reasoning.effort").String())
	require.Equal(t, "foo", gjson.GetBytes(normalized, "include.0").String())
	require.Equal(t, "reasoning.encrypted_content", gjson.GetBytes(normalized, "include.1").String())

	withInclude := []byte(`{"model":"gpt-5.4-mini","input":[{"type":"message","role":"user","content":"hi"}],"include":["reasoning.encrypted_content"]}`)
	normalized, _, err = normalizeOpenAIPassthroughOAuthBody(withInclude, false)
	require.NoError(t, err)
	require.Equal(t, 1, len(gjson.GetBytes(normalized, "include").Array()))

	compact := []byte(`{"model":"gpt-5.4-mini","input":[{"type":"message","role":"user","content":"hi"}]}`)
	normalized, _, err = normalizeOpenAIPassthroughOAuthBody(compact, true)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(normalized, "reasoning.summary").Exists(), "compact 不强制 reasoning.summary")
	require.False(t, gjson.GetBytes(normalized, "include").Exists(), "compact 不强制 include")
}

func TestDetectOpenAIPassthroughInstructionsRejectReason(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{name: "missing is optional", body: `{"model":"gpt-5.1-codex"}`, want: ""},
		{name: "non string remains rejected", body: `{"instructions":{"text":"invalid"}}`, want: "instructions_not_string"},
		{name: "empty remains rejected", body: `{"instructions":"  "}`, want: "instructions_empty"},
		{name: "non empty remains accepted", body: `{"instructions":"client guidance"}`, want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, detectOpenAIPassthroughInstructionsRejectReason("gpt-5.1-codex", []byte(tt.body)))
		})
	}
}

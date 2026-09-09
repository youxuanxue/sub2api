package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCleanGeminiNativeThoughtSignatures_ReplacesPartSignatures(t *testing.T) {
	input := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "hello"}]
			},
			{
				"role": "model",
				"parts": [
					{"text": "thinking", "thought": true, "thoughtSignature": "sig_1"},
					{"functionCall": {"name": "toolA", "args": {"k": "v"}}, "thoughtSignature": "sig_2"}
				]
			}
		],
		"cachedContent": {
			"parts": [{"text": "cached", "thoughtSignature": "sig_3"}]
		},
		"signature": "keep_me"
	}`)

	cleaned := CleanGeminiNativeThoughtSignatures(input)

	var got map[string]any
	require.NoError(t, json.Unmarshal(cleaned, &got))

	require.Equal(t, antigravity.DummyThoughtSignature, gjson.GetBytes(cleaned, "contents.1.parts.0.thoughtSignature").String())
	require.Equal(t, antigravity.DummyThoughtSignature, gjson.GetBytes(cleaned, "contents.1.parts.1.thoughtSignature").String())
	require.Equal(t, "sig_3", gjson.GetBytes(cleaned, "cachedContent.parts.0.thoughtSignature").String())
	require.Equal(t, "keep_me", gjson.GetBytes(cleaned, "signature").String())
}

func TestCleanGeminiNativeThoughtSignatures_PreservesToolData(t *testing.T) {
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"id":9007199254740993,"thoughtSignature":"business-value"}},"thoughtSignature":"old-signature"}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"id":9007199254740993,"thoughtSignature":"tool-result"}}},{"inlineData":{"mimeType":"image/png","data":"test-image"}}]}],"tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"object","properties":{"thoughtSignature":{"type":"string"}}}}]}]}`)
	cleaned := CleanGeminiNativeThoughtSignatures(input)
	require.Equal(t, antigravity.DummyThoughtSignature, gjson.GetBytes(cleaned, "contents.0.parts.0.thoughtSignature").String())
	for _, path := range []string{"contents.0.parts.0.functionCall", "contents.1", "tools"} {
		require.JSONEq(t, gjson.GetBytes(input, path).Raw, gjson.GetBytes(cleaned, path).Raw)
	}
	require.Equal(t, "9007199254740993", gjson.GetBytes(cleaned, "contents.0.parts.0.functionCall.args.id").Raw)
}

func TestCleanGeminiNativeThoughtSignatures_InvalidJSONReturnsOriginal(t *testing.T) {
	input := []byte(`{"contents":[invalid-json]}`)

	cleaned := CleanGeminiNativeThoughtSignatures(input)

	require.Equal(t, input, cleaned)
}

func TestCleanGeminiNativeThoughtSignatures_NoProtocolSignatureIsNoOp(t *testing.T) {
	input := []byte(`{ "contents": [{"role":"user","parts":[{"text":"hi"}]}], "thoughtSignature": "opaque" }`)
	require.Equal(t, input, CleanGeminiNativeThoughtSignatures(input))
}

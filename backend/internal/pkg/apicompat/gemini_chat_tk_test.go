package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeminiToChatRequest(t *testing.T) {
	body := []byte(`{"systemInstruction":{"parts":[{"text":"Be precise"}]},"contents":[{"role":"user","parts":[{"text":"Hel"},{"text":"lo"}]},{"role":"model","parts":[{"text":"Hi"}]}],"generationConfig":{"temperature":0,"topP":0.9,"maxOutputTokens":64,"stopSequences":["END"],"candidateCount":1}}`)
	r, err := GeminiToChatRequest(body, "client-alias", true)
	require.NoError(t, err)
	require.Equal(t, "client-alias", r.Model)
	require.True(t, r.StreamOptions.IncludeUsage)
	require.Len(t, r.Messages, 3)
	require.Equal(t, "system", r.Messages[0].Role)
	require.JSONEq(t, `"Hello"`, string(r.Messages[1].Content))
	require.Equal(t, "assistant", r.Messages[2].Role)
	require.Equal(t, 0.0, *r.Temperature)
	require.Equal(t, 64, *r.MaxTokens)
	require.JSONEq(t, `["END"]`, string(r.Stop))
}

func TestGeminiToChatRejectsUnsupportedSemantics(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `[]`,
		`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}]}`,
		`{"contents":[{"parts":[{"text":"hi","thoughtSignature":"opaque"}]}]}`,
		`{"contents":[{"parts":[{"functionResponse":{"name":"x"}}]}]}`,
		`{"contents":[{"parts":[{"text":1}]}]}`,
		`{"contents":[{"parts":[{}]}]}`,
		`{"contents":[{"role":"system","parts":[{"text":"hi"}]}]}`,
	} {
		t.Run(body, func(t *testing.T) { _, err := GeminiToChatRequest([]byte(body), "model", false); require.Error(t, err) })
	}
	for _, extra := range []string{
		`"tools":[]`, `"toolConfig":{}`, `"cachedContent":"cache"`, `"safetySettings":[]`, `"unknown":true`,
		`"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}`,
		`"generationConfig":{"responseMimeType":"application/json"}`,
		`"generationConfig":{"topK":10}`, `"generationConfig":{"candidateCount":2}`,
		`"generationConfig":{"maxOutputTokens":0}`, `"generationConfig":{"temperature":3}`,
		`"generationConfig":{"topP":-1}`, `"generationConfig":{"stopSequences":[""]}`,
	} {
		t.Run(extra, func(t *testing.T) {
			_, err := GeminiToChatRequest([]byte(`{"contents":[{"parts":[{"text":"hi"}]}],`+extra+`}`), "model", false)
			require.Error(t, err)
		})
	}
}

func TestChatToGeminiResponse(t *testing.T) {
	r, err := ChatToGeminiResponse([]byte(`{"choices":[{"index":0,"message":{"content":"hello","reasoning_content":"thinking"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30,"prompt_tokens_details":{"cached_tokens":5},"completion_tokens_details":{"reasoning_tokens":3}}}`), false)
	require.NoError(t, err)
	b, err := json.Marshal(r)
	require.NoError(t, err)
	require.JSONEq(t, `{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"thinking","thought":true},{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":7,"totalTokenCount":30,"cachedContentTokenCount":5,"thoughtsTokenCount":3}}`, string(b))
	for _, finish := range []string{"length", "content_filter"} {
		r, err = ChatToGeminiResponse([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"`+finish+`"}]}`), true)
		require.NoError(t, err)
		require.NotEmpty(t, r.Candidates[0].FinishReason)
	}
}

func TestChatToGeminiRejectsInvalidOutput(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `{"error":{"message":"secret"}}`,
		`{"choices":[{"message":{"content":"partial"}}]}`,
		`{"choices":[{"message":{"tool_calls":[{}]},"finish_reason":"stop"}]}`,
		`{"choices":[{"message":{"content":[]},"finish_reason":"stop"}]}`,
		`{"choices":[{"message":{"content":"hi"},"finish_reason":"unknown"}]}`,
	} {
		_, err := ChatToGeminiResponse([]byte(body), false)
		require.Error(t, err, body)
	}
}

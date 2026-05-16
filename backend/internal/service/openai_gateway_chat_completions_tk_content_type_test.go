package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Regression pin for Wei-Shaw/sub2api#1311.
//
// /v1/chat/completions non-stream requests forward to upstream /v1/responses
// SSE, buffer the stream, and convert the terminal event into a single JSON
// chat.completion object. The response header propagation step copies the
// upstream `Content-Type: text/event-stream` header to the client; gin's
// c.JSON does NOT overwrite an already-set Content-Type, so without an
// explicit override the client receives a JSON body whose `Content-Type` is
// still `text/event-stream`, breaking OpenAI SDK consumers that branch on
// stream vs. non-stream by header.
//
// The fix forces `Content-Type: application/json` after WriteFilteredHeaders
// in handleChatBufferedStreamingResponse, mirroring the explicit override the
// streaming sibling already does (`text/event-stream`).

func TestForwardAsChatCompletions_NonStream_ReturnsApplicationJSONContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := []byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":17,"output_tokens":8,"total_tokens":25}}}` + "\n\n")
	upstreamStream := newOpenAICompatBlockingReadCloser(upstreamBody)
	defer func() {
		require.NoError(t, upstreamStream.Close())
	}()
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		// Upstream Responses API is SSE. The bug was that this Content-Type
		// leaked to the downstream client even though the buffered handler
		// emits a JSON body.
		Header: http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_nonstream_content_type"}},
		Body:   upstreamStream,
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          1,
		Name:        "openai-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}

	type forwardResult struct {
		result *OpenAIForwardResult
		err    error
	}
	resultCh := make(chan forwardResult, 1)
	go func() {
		result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.1")
		resultCh <- forwardResult{result: result, err: err}
	}()

	select {
	case got := <-resultCh:
		require.NoError(t, got.err)
		require.NotNil(t, got.result)
		require.False(t, got.result.Stream, "non-stream request must produce non-stream result")
		gotCT := rec.Header().Get("Content-Type")
		require.True(t, strings.HasPrefix(gotCT, "application/json"),
			"non-stream /v1/chat/completions must emit application/json Content-Type; got %q. See Wei-Shaw/sub2api#1311.", gotCT)
		require.NotContains(t, gotCT, "text/event-stream",
			"Content-Type must not be SSE; got %q. See Wei-Shaw/sub2api#1311.", gotCT)
		require.Contains(t, rec.Body.String(), `"finish_reason":"stop"`, "response body should be JSON, not SSE")
		require.NotContains(t, rec.Body.String(), "data:", "response body must not be SSE-framed")
	case <-time.After(time.Second):
		require.Fail(t, "ForwardAsChatCompletions buffered response did not return in time")
	}
}

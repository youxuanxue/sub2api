//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiForwardAsResponses_FailedAttemptRequestIDDoesNotLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gemini-3-flash","input":"hi"}`)
	account := geminiCompatAntigravityRelayStub(85)
	account.Credentials["api_key"] = "relay-key"

	for _, tt := range []struct {
		name      string
		successID string
		wantID    string
	}{
		{name: "successful attempt replaces failed id", successID: "healthy-id", wantID: "healthy-id"},
		{name: "successful attempt without id leaves header empty"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

			failedSvc := &GeminiMessagesCompatService{
				httpUpstream: &geminiCompatHTTPUpstreamStub{response: geminiCompatResponse(
					http.StatusServiceUnavailable,
					"failed-relay-id",
					geminiCompatEmptyPoolBody(),
				)},
				cfg: &config.Config{},
			}
			failedResult, err := failedSvc.ForwardAsResponses(context.Background(), c, account, body)
			require.Nil(t, failedResult)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Empty(t, c.Writer.Header().Get("x-request-id"))

			healthySvc := &GeminiMessagesCompatService{
				httpUpstream: &geminiCompatHTTPUpstreamStub{response: geminiCompatResponse(
					http.StatusOK,
					tt.successID,
					`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
				)},
				cfg: &config.Config{},
			}
			result, err := healthySvc.ForwardAsResponses(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tt.wantID, c.Writer.Header().Get("x-request-id"))
			require.NotEqual(t, "failed-relay-id", c.Writer.Header().Get("x-request-id"))
		})
	}
}

func TestGeminiForwardAsResponses_GeminiNonStreamReturnsResponsesJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamBody := `{"candidates":[{"content":{"parts":[{"text":"hello from gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}}`
	httpStub := &geminiCompatHTTPUpstreamStub{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		},
	}
	svc := &GeminiMessagesCompatService{
		httpUpstream: httpStub,
		cfg:          &config.Config{},
	}
	account := &Account{
		ID:       301,
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "gemini-api-key",
		},
		Concurrency: 1,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gemini-2.5-flash","input":"hi"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	result, err := svc.ForwardAsResponses(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gemini-2.5-flash", result.Model)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.False(t, result.Stream)

	require.NotNil(t, httpStub.lastReq)
	require.Contains(t, httpStub.lastReq.URL.String(), "/v1beta/models/gemini-2.5-flash:generateContent")
	require.Equal(t, "gemini-api-key", httpStub.lastReq.Header.Get("x-goog-api-key"))

	out := rec.Body.String()
	require.Contains(t, out, `"object":"response"`)
	require.Contains(t, out, `"model":"gemini-2.5-flash"`)
	require.Contains(t, out, "hello from gemini")
	require.Contains(t, out, `"input_tokens":7`)
	require.Contains(t, out, `"output_tokens":3`)
}

func TestGeminiForwardAsResponses_GeminiStreamEmitsResponsesSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamBody := `data: {"candidates":[{"content":{"parts":[{"text":"hel"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}` + "\n\n" +
		`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2}}` + "\n\n" +
		"data: [DONE]\n\n"
	httpStub := &geminiCompatHTTPUpstreamStub{
		response: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(upstreamBody)),
		},
	}
	svc := &GeminiMessagesCompatService{
		httpUpstream: httpStub,
		cfg:          &config.Config{},
	}
	account := &Account{
		ID:       302,
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "gemini-api-key",
		},
		Concurrency: 1,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gemini-2.5-flash","stream":true,"input":"hi"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	result, err := svc.ForwardAsResponses(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 2, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)

	require.NotNil(t, httpStub.lastReq)
	require.Contains(t, httpStub.lastReq.URL.String(), "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse")

	out := rec.Body.String()
	require.Contains(t, out, "event: response.created")
	require.Contains(t, out, `"type":"response.output_text.delta"`)
	require.Contains(t, out, `"delta":"hel"`)
	require.Contains(t, out, `"delta":"lo"`)
	require.Contains(t, out, `"type":"response.completed"`)
	require.Contains(t, out, "data: [DONE]")
}

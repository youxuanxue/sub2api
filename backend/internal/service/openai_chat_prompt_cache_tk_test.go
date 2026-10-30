//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestChatResponsesPromptCacheCapabilities(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		t.Run(map[bool]string{false: "api_key", true: "codex"}[oauth], func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.6","prompt_cache_key":"client-key","prompt_cache_retention":"24h","prompt_cache_options":{"mode":"explicit"},"messages":[{"role":"user","content":[{"type":"text","text":"hello","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`)
			upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"message":"test stop"}}`)}
			account := newOpenAIRejectedFieldTestAccount()
			if oauth {
				account.Type = AccountTypeOAuth
				account.Credentials = map[string]any{"access_token": "token", "chatgpt_account_id": "acct"}
			}
			_, err := newOpenAIRejectedFieldTestService(upstream).ForwardAsChatCompletions(context.Background(), newOpenAIRejectedFieldTestContext(body), account, body, "", "")
			require.Error(t, err)
			require.Len(t, upstream.bodies, 1)
			wire := upstream.lastBody
			expectedKey := "client-key"
			if oauth {
				expectedKey = scopeCodexAccountIdentityValue(account, 0, "prompt-cache", expectedKey)
			}
			require.Equal(t, expectedKey, gjson.GetBytes(wire, "prompt_cache_key").String())
			require.Equal(t, !oauth, gjson.GetBytes(wire, "prompt_cache_options").Exists())
			require.Equal(t, !oauth, gjson.GetBytes(wire, "input.0.content.0.prompt_cache_breakpoint").Exists())
			require.Equal(t, !oauth, gjson.GetBytes(wire, "prompt_cache_retention").Exists())
		})
	}
}

func TestChatResponsesRetriesRejectedExplicitCacheHints(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","prompt_cache_key":"client-key","prompt_cache_options":{"mode":"explicit"},"messages":[{"role":"user","content":[{"type":"text","text":"hello","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: prompt_cache_breakpoint"}}`),
		&http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"id":"resp-cache","object":"response","model":"gpt-5.6","status":"completed","output":[{"type":"message","id":"msg-cache","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":5,"total_tokens":16,"input_tokens_details":{"cached_tokens":4}}}}

`))},
	}}
	result, err := newOpenAIRejectedFieldTestService(upstream).ForwardAsChatCompletions(context.Background(), newOpenAIRejectedFieldTestContext(body), newOpenAIRejectedFieldTestAccount(), body, "client-key", "")
	require.NoError(t, err)
	require.Equal(t, 4, result.Usage.CacheReadInputTokens)
	require.Len(t, upstream.bodies, 2)
	require.True(t, gjson.GetBytes(upstream.bodies[0], "prompt_cache_options").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "prompt_cache_options").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.content.0.prompt_cache_breakpoint").Exists())
	require.Equal(t, "client-key", gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String())
	require.Equal(t, upstream.requests[0].Header.Get("session_id"), upstream.requests[1].Header.Get("session_id"))
}

func TestOpenAIPromptCacheHintsOnlyChangeProtocolFields(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"keep-key","prompt_cache_retention":"24h","prompt_cache_options":{"mode":"explicit"},"prompt_cache_breakpoint":true,"input":[{"prompt_cache_breakpoint":true,"content":[{"type":"input_text","text":"keep text","prompt_cache_breakpoint":{}}],"arguments":"{\"prompt_cache_breakpoint\":true}"}],"messages":[{"content":[{"prompt_cache_breakpoint":true}]}],"tools":[{"parameters":{"properties":{"prompt_cache_breakpoint":{"type":"string"},"prompt_cache_options":{"type":"object"}}}}]}`)
	for _, errBody := range []string{
		`{"error":{"message":"Unsupported parameter: prompt_cache_breakpoint"}}`,
		`{"error":{"message":"prompt_cache_options is not supported on this model"}}`,
		`{"error":{"message":"Missing required parameter: 'input[0].content[0].prompt_cache_breakpoint.mode'"}}`,
	} {
		next, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, []byte(errBody))
		require.NoError(t, err)
		require.True(t, changed)
		for _, path := range []string{"prompt_cache_options", "prompt_cache_breakpoint", "input.0.prompt_cache_breakpoint", "input.0.content.0.prompt_cache_breakpoint", "messages.0.content.0.prompt_cache_breakpoint"} {
			require.False(t, gjson.GetBytes(next, path).Exists(), path)
		}
		for _, path := range []string{"prompt_cache_key", "prompt_cache_retention", "tools", "input.0.arguments", "input.0.content.0.text"} {
			require.Equal(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(next, path).Raw, path)
		}
	}
	implicit := []byte(`{"prompt_cache_options":{"ttl":"30m"},"prompt_cache_key":"keep"}`)
	next, changed, err := stripOpenAIExplicitPromptCacheHints(implicit)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(implicit), string(next))
}

func TestOpenAIPromptCacheHintsRejectAmbiguousErrors(t *testing.T) {
	body := []byte(`{"prompt_cache_options":{"mode":"explicit"},"input":[{"content":[{"prompt_cache_breakpoint":{}}]}]}`)
	for _, errBody := range []string{
		`{"error":{"code":"unsupported_parameter","param":"prompt_cache_options","message":"Unsupported parameter: input[0].content[0].prompt_cache_breakpoint"}}`,
		`{"error":{"code":"unsupported_parameter","param":"input[8].content[0].prompt_cache_breakpoint"}}`,
		`{"error":{"code":"unsupported_parameter","param":"tools[0].parameters.prompt_cache_breakpoint"}}`,
		`{"error":{"message":"The user mentioned prompt_cache_options"}}`,
		`{"error":{"message":"tools[0].parameters.prompt_cache_options is not supported on this model"}}`,
	} {
		_, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, []byte(errBody))
		require.NoError(t, err)
		require.False(t, changed, errBody)
	}
}

func TestChatResponsesShapeRetainsAPIKeyRetention(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","prompt_cache_key":"raw-key","prompt_cache_retention":"24h","prompt_cache_options":{"mode":"explicit"},"input":[{"role":"user","content":[{"type":"input_text","text":"hello","prompt_cache_breakpoint":{}}]}]}`)
	upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"message":"test stop"}}`)}
	_, err := newOpenAIRejectedFieldTestService(upstream).ForwardAsChatCompletions(context.Background(), newOpenAIRejectedFieldTestContext(body), newOpenAIRejectedFieldTestAccount(), body, "", "")
	require.Error(t, err)
	require.Equal(t, "24h", gjson.GetBytes(upstream.lastBody, "prompt_cache_retention").String())
	require.Equal(t, "raw-key", gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, "explicit", gjson.GetBytes(upstream.lastBody, "prompt_cache_options.mode").String())
}

func TestChatResponsesRejectedHintsRespectSharedBudget(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","prompt_cache_options":{"mode":"explicit"},"messages":[{"role":"user","content":"hello"}]}`)
	c := newOpenAIRejectedFieldTestContext(body)
	state := openAIResponsesRejectedFieldRetryStateForRequest(c, body)
	for i := 0; i < maxOpenAIResponsesRejectedFieldRetries; i++ {
		require.True(t, state.Allow([]byte(strings.Repeat("x", i+1))))
	}
	upstream := &httpUpstreamRecorder{resp: newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"error":{"message":"Unsupported parameter: prompt_cache_options"}}`)}
	_, err := newOpenAIRejectedFieldTestService(upstream).ForwardAsChatCompletions(context.Background(), c, newOpenAIRejectedFieldTestAccount(), body, "", "")
	require.Error(t, err)
	require.Len(t, upstream.bodies, 1)
}

// This reader reproduces cancellation or a transport truncation during the
// error-body read, after the forwarder's initial retry eligibility check.
type chatCacheInterruptedErrorBody struct {
	io.Reader
	cancel    context.CancelFunc
	truncated bool
}

func (b *chatCacheInterruptedErrorBody) Read(p []byte) (int, error) {
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	n, err := b.Reader.Read(p)
	if err == io.EOF && b.truncated {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
func (*chatCacheInterruptedErrorBody) Close() error { return nil }

func TestChatResponsesRejectedHintsDoNotRetryInterruptedErrorBody(t *testing.T) {
	for _, mode := range []string{"forward_context_cancelled", "client_context_cancelled", "truncated_body"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.6","prompt_cache_options":{"mode":"explicit"},"messages":[{"role":"user","content":"hello"}]}`)
			c := newOpenAIRejectedFieldTestContext(body)
			c.Request.URL.Path = "/v1/chat/completions"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			forwardCtx := context.Background()
			errorBody := &chatCacheInterruptedErrorBody{Reader: strings.NewReader(`{"error":{"message":"Unsupported parameter: prompt_cache_options"}}`)}
			switch mode {
			case "forward_context_cancelled":
				forwardCtx, errorBody.cancel = ctx, cancel
			case "client_context_cancelled":
				c.Request = c.Request.WithContext(ctx)
				errorBody.cancel = cancel
			case "truncated_body":
				errorBody.truncated = true
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: errorBody,
			}}
			_, err := newOpenAIRejectedFieldTestService(upstream).ForwardAsChatCompletions(forwardCtx, c, newOpenAIRejectedFieldTestAccount(), body, "", "")
			require.Error(t, err)
			require.Len(t, upstream.requests, 1)
			budget, ok := c.Get(openAIResponsesRejectedFieldRetryBudgetContextKey)
			require.True(t, ok)
			require.Zero(t, budget.(*openAIResponsesRejectedFieldRetryBudget).attempts)
		})
	}
}

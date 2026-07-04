//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapidto "github.com/QuantumNous/new-api/dto"
	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardResponses_ForceChatCompletionsRoutesNonStreamingToChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_resp_chat_json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_json","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":1}}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "http://upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
	require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.Equal(t, "response", gjson.Get(rec.Body.String(), "object").String())
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.False(t, result.Stream)
}

func TestForwardResponses_ForceChatCompletionsRoutesStreamingToChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"he"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		`data: {"id":"chatcmpl_stream","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_resp_chat_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "http://upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	require.Contains(t, rec.Body.String(), "event: response.output_text.delta")
	require.Contains(t, rec.Body.String(), `"delta":"he"`)
	require.Contains(t, rec.Body.String(), "event: response.completed")
	require.Contains(t, rec.Body.String(), `"input_tokens":4`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.True(t, result.Stream)
	require.NotNil(t, result.FirstTokenMs)
}

func TestForwardResponses_DeepSeekReasoningOnlyStreamProducesVisibleText(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-reasoner","input":"hello","stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_reasoning","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_reasoning","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"reasoning_content":"visible fallback"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_reasoning","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_deepseek_reasoning_responses_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Contains(t, rec.Body.String(), "event: response.output_text.delta")
	require.Contains(t, rec.Body.String(), `"delta":"visible fallback"`)
	require.Contains(t, rec.Body.String(), `"status":"incomplete"`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}

func TestForwardResponses_AutoSupportedAccountStillUsesResponsesEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","input":"hello","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_resp_native"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_native","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}],"status":"completed"}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode:      string(openai_compat.ResponsesSupportModeAuto),
		openai_compat.ExtraKeyResponsesSupported: true,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "http://upstream.example/v1/responses", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
}

func TestForwardAsResponsesDispatched_NewAPIConvertNotImplementedFallsBackToChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldResponses := dispatchNewAPIResponses
	oldChat := dispatchNewAPIChatCompletions
	t.Cleanup(func() {
		dispatchNewAPIResponses = oldResponses
		dispatchNewAPIChatCompletions = oldChat
	})

	dispatchNewAPIResponses = func(context.Context, *gin.Context, bridge.ChannelContextInput, []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		return nil, newapitypes.NewError(errors.New("not implemented"), newapitypes.ErrorCodeConvertRequestFailed, newapitypes.ErrOptionWithSkipRetry())
	}

	var capturedPath string
	var capturedChannelType int
	var capturedChatBody []byte
	dispatchNewAPIChatCompletions = func(_ context.Context, c *gin.Context, in bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		capturedPath = c.Request.URL.Path
		capturedChannelType = in.ChannelType
		capturedChatBody = append([]byte(nil), body...)
		c.Writer.Header().Set("Content-Type", "application/json")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write([]byte(`{"id":"chatcmpl_bridge_fallback","object":"chat.completion","model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
		return &bridge.DispatchOutcome{
			Usage:         &newapidto.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
			Model:         "deepseek-chat",
			UpstreamModel: "deepseek-chat",
			Stream:        false,
		}, nil
	}

	body := []byte(`{"model":"deepseek-chat","input":"hello","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	svc := &OpenAIGatewayService{}
	account := &Account{
		ID:          4301,
		Name:        "newapi-deepseek",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 43,
		Credentials: map[string]any{
			"api_key":  "sk-newapi",
			"base_url": "https://newapi.example",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.ForwardAsResponsesDispatched(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "response", gjson.Get(rec.Body.String(), "object").String())
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
	require.Equal(t, "/v1/chat/completions", capturedPath)
	require.Equal(t, "/v1/responses", c.Request.URL.Path)
	require.Equal(t, 43, capturedChannelType)
	require.False(t, gjson.GetBytes(capturedChatBody, "input").Exists())
	require.Equal(t, "hello", gjson.GetBytes(capturedChatBody, "messages.0.content").String())
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestForwardAsResponsesDispatched_NewAPIStreamOnlyModelBuffersNonStreamingClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldResponses := dispatchNewAPIResponses
	oldChat := dispatchNewAPIChatCompletions
	t.Cleanup(func() {
		dispatchNewAPIResponses = oldResponses
		dispatchNewAPIChatCompletions = oldChat
	})

	dispatchNewAPIResponses = func(context.Context, *gin.Context, bridge.ChannelContextInput, []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		return nil, newapitypes.NewError(errors.New("not implemented"), newapitypes.ErrorCodeConvertRequestFailed, newapitypes.ErrOptionWithSkipRetry())
	}

	var capturedChatBody []byte
	dispatchNewAPIChatCompletions = func(_ context.Context, c *gin.Context, _ bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		capturedChatBody = append([]byte(nil), body...)
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write([]byte(strings.Join([]string{
			`data: {"id":"chatcmpl_glm","object":"chat.completion.chunk","model":"glm-4.5","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`,
			"",
			`data: {"id":"chatcmpl_glm","object":"chat.completion.chunk","model":"glm-4.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			"",
			`data: {"id":"chatcmpl_glm","object":"chat.completion.chunk","model":"glm-4.5","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")))
		return &bridge.DispatchOutcome{
			Usage:         &newapidto.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6},
			Model:         "glm-4.5",
			UpstreamModel: "glm-4.5",
			Stream:        true,
		}, nil
	}

	body := []byte(`{"model":"glm-4.5","input":"hello","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	svc := &OpenAIGatewayService{}
	account := &Account{
		ID:          4302,
		Name:        "newapi-glm",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 43,
		Credentials: map[string]any{
			"api_key":  "sk-newapi",
			"base_url": "https://newapi.example",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.ForwardAsResponsesDispatched(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "response", gjson.Get(rec.Body.String(), "object").String())
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "output.0.content.0.text").String())
	require.True(t, gjson.GetBytes(capturedChatBody, "stream").Bool())
	require.True(t, gjson.GetBytes(capturedChatBody, "stream_options.include_usage").Bool())
	require.False(t, result.Stream)
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestShouldFallbackNewAPIResponsesToChat(t *testing.T) {
	cases := []struct {
		name   string
		apiErr *newapitypes.NewAPIError
		want   bool
	}{
		{
			name: "nil error",
			want: false,
		},
		{
			name: "convert not implemented",
			apiErr: newapitypes.NewError(
				errors.New("convert request failed: not implemented"),
				newapitypes.ErrorCodeConvertRequestFailed,
				newapitypes.ErrOptionWithSkipRetry(),
			),
			want: true,
		},
		{
			name: "upstream 400 not supported",
			apiErr: newapitypes.NewError(
				errors.New("upstream status code: 400, model not supported"),
				newapitypes.ErrorCodeConvertRequestFailed,
				newapitypes.ErrOptionWithSkipRetry(),
			),
			want: true,
		},
		{
			name: "upstream unsupported model wording",
			apiErr: newapitypes.NewError(
				errors.New("upstream status code: 400, Unsupported model: 'glm-4.5'"),
				newapitypes.ErrorCodeConvertRequestFailed,
				newapitypes.ErrOptionWithSkipRetry(),
			),
			want: true,
		},
		{
			name: "upstream 404 stream mode",
			apiErr: newapitypes.NewError(
				errors.New("upstream status code: 404, this model only support stream mode"),
				newapitypes.ErrorCodeConvertRequestFailed,
				newapitypes.ErrOptionWithSkipRetry(),
			),
			want: true,
		},
		{
			name: "upstream 400 enable thinking restricted",
			apiErr: newapitypes.NewError(
				errors.New("upstream status code: 400, enable_thinking parameter is restricted"),
				newapitypes.ErrorCodeConvertRequestFailed,
				newapitypes.ErrOptionWithSkipRetry(),
			),
			want: true,
		},
		{
			name: "upstream 404 invalid model",
			apiErr: newapitypes.NewError(
				errors.New("upstream status code: 404, invalid model"),
				newapitypes.ErrorCodeConvertRequestFailed,
				newapitypes.ErrOptionWithSkipRetry(),
			),
			want: true,
		},
		{
			name: "convert request failed marker",
			apiErr: newapitypes.NewError(
				errors.New("convert request failed: upstream rejected request"),
				newapitypes.ErrorCodeConvertRequestFailed,
				newapitypes.ErrOptionWithSkipRetry(),
			),
			want: true,
		},
		{
			name: "non matching error",
			apiErr: newapitypes.NewError(
				errors.New("status code: 400 bad request"),
				newapitypes.ErrorCodeConvertRequestFailed,
				newapitypes.ErrOptionWithSkipRetry(),
			),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldFallbackNewAPIResponsesToChat(tc.apiErr))
		})
	}
}

func TestApplyNewAPIResponsesChatFallbackShape(t *testing.T) {
	baseBody := []byte(`{"model":"placeholder","stream":false,"messages":[{"role":"user","content":"hi"}]}`)

	t.Run("qwen3 sets enable_thinking false", func(t *testing.T) {
		shaped := applyNewAPIResponsesChatFallbackShape("qwen3-8b", baseBody)
		require.Equal(t, false, gjson.GetBytes(shaped, "enable_thinking").Bool())
		require.Equal(t, false, gjson.GetBytes(shaped, "stream").Bool())
	})

	t.Run("qwen3 preview sets stream and thinking", func(t *testing.T) {
		shaped := applyNewAPIResponsesChatFallbackShape("qwen3.7-max-preview", baseBody)
		require.Equal(t, true, gjson.GetBytes(shaped, "stream").Bool())
		require.Equal(t, true, gjson.GetBytes(shaped, "enable_thinking").Bool())
	})

	t.Run("qwen3 preview dated variant sets stream and thinking", func(t *testing.T) {
		shaped := applyNewAPIResponsesChatFallbackShape("qwen3.7-max-2026-05-17", baseBody)
		require.Equal(t, true, gjson.GetBytes(shaped, "stream").Bool())
		require.Equal(t, true, gjson.GetBytes(shaped, "enable_thinking").Bool())
	})

	t.Run("glm-4.5 forces stream true", func(t *testing.T) {
		shaped := applyNewAPIResponsesChatFallbackShape("glm-4.5", baseBody)
		require.Equal(t, true, gjson.GetBytes(shaped, "stream").Bool())
		require.False(t, gjson.GetBytes(shaped, "enable_thinking").Exists())
	})

	t.Run("glm-4.5-air forces stream true", func(t *testing.T) {
		shaped := applyNewAPIResponsesChatFallbackShape("glm-4.5-air", baseBody)
		require.Equal(t, true, gjson.GetBytes(shaped, "stream").Bool())
		require.False(t, gjson.GetBytes(shaped, "enable_thinking").Exists())
	})

	t.Run("other model remains unchanged", func(t *testing.T) {
		shaped := applyNewAPIResponsesChatFallbackShape("gpt-4o", baseBody)
		require.Equal(t, string(baseBody), string(shaped))
	})

	t.Run("invalid json remains unchanged", func(t *testing.T) {
		invalid := []byte(`{"model":"qwen3-8b"`)
		shaped := applyNewAPIResponsesChatFallbackShape("qwen3-8b", invalid)
		require.Equal(t, string(invalid), string(shaped))
	})
}

func forceChatResponsesFallbackAccount() *Account {
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
	}
	return account
}

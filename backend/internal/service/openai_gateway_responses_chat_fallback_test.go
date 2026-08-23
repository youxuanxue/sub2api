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
	"sync"
	"testing"
	"time"

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

func TestForwardResponses_PassthroughFlagWithUnsupportedResponsesUsesAccountMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		path := path
		t.Run(path, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4-channel","input":"hello","stream":false}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"chatcmpl_mapping","object":"chat.completion","model":"gpt-5.4-account","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
				)),
			}}
			svc := &OpenAIGatewayService{
				cfg:          rawChatCompletionsTestConfig(),
				httpUpstream: upstream,
			}
			account := rawChatCompletionsTestAccount()
			account.Credentials["model_mapping"] = map[string]any{
				"gpt-5.4-channel": "gpt-5.4-account",
			}
			account.Credentials["compact_model_mapping"] = map[string]any{
				"gpt-5.4-account": "gpt-5.4-compact",
			}
			account.Extra = map[string]any{
				"openai_passthrough":                     true,
				openai_compat.ExtraKeyResponsesSupported: false,
			}

			result, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "http://upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
			require.Equal(t, "gpt-5.4-account", gjson.GetBytes(upstream.lastBody, "model").String())
		})
	}
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

func TestForwardResponses_ChatFallbackRejectsInvalidToolArgumentsAtOutputLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-v4-flash","input":"run the command","stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_length_tool","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_length","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"ssh root@HOST"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_length_tool","object":"chat.completion.chunk","model":"deepseek-v4-flash","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":4,"completion_tokens":6492,"total_tokens":6496}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_length_tool"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.ErrorContains(t, err, "invalid JSON")
	require.NotNil(t, result)
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Equal(t, 6492, result.Usage.OutputTokens)
	require.NotContains(t, rec.Body.String(), "response.function_call_arguments.done")
	require.NotContains(t, rec.Body.String(), "response.output_item.done")
	require.NotContains(t, rec.Body.String(), "data: [DONE]")
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

func TestForwardAsResponsesDispatched_ProactiveChatFallbackSkipsResponsesAdaptor(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldResponses := dispatchNewAPIResponses
	oldChat := dispatchNewAPIChatCompletions
	t.Cleanup(func() {
		dispatchNewAPIResponses = oldResponses
		dispatchNewAPIChatCompletions = oldChat
	})

	responsesCalled := false
	dispatchNewAPIResponses = func(context.Context, *gin.Context, bridge.ChannelContextInput, []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		responsesCalled = true
		return nil, newapitypes.NewError(errors.New("should not call responses adaptor"), newapitypes.ErrorCodeConvertRequestFailed, newapitypes.ErrOptionWithSkipRetry())
	}

	dispatchNewAPIChatCompletions = func(_ context.Context, c *gin.Context, _ bridge.ChannelContextInput, _ []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		c.Writer.Header().Set("Content-Type", "application/json")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write([]byte(`{"id":"chatcmpl_proactive","object":"chat.completion","model":"glm-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
		return &bridge.DispatchOutcome{
			Usage:         &newapidto.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
			Model:         "glm-4.5",
			UpstreamModel: "glm-4.5",
			Stream:        false,
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
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 43,
		Credentials: map[string]any{"api_key": "sk-newapi", "base_url": "https://newapi.example"},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.ForwardAsResponsesDispatched(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, responsesCalled)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "response", gjson.Get(rec.Body.String(), "object").String())
}

func TestForwardAsResponsesDispatched_Qwen38StreamOnlyModelBuffersNonStreamingClient(t *testing.T) {
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
			`data: {"id":"chatcmpl_qwen38","object":"chat.completion.chunk","model":"qwen3.8-2.4t-a95b","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`,
			"",
			`data: {"id":"chatcmpl_qwen38","object":"chat.completion.chunk","model":"qwen3.8-2.4t-a95b","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			"",
			`data: {"id":"chatcmpl_qwen38","object":"chat.completion.chunk","model":"qwen3.8-2.4t-a95b","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")))
		return &bridge.DispatchOutcome{
			Usage:         &newapidto.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6},
			Model:         "qwen3.8-2.4t-a95b",
			UpstreamModel: "qwen3.8-2.4t-a95b",
			Stream:        true,
		}, nil
	}

	body := []byte(`{"model":"qwen3.8-2.4t-a95b","input":"hello","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	svc := &OpenAIGatewayService{}
	account := &Account{
		ID:          4302,
		Name:        "newapi-qwen38",
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
	require.True(t, gjson.GetBytes(capturedChatBody, "enable_thinking").Bool())
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
			name: "unsupported model without status code hint",
			apiErr: newapitypes.NewError(
				errors.New("unsupported model: glm-4.5"),
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
			name: "unsupported model without status code hint",
			apiErr: newapitypes.NewError(
				errors.New("unsupported model for responses endpoint"),
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

	t.Run("qwen3.8 large variant sets stream and thinking", func(t *testing.T) {
		shaped := applyNewAPIResponsesChatFallbackShape("qwen3.8-2.4t-a95b", baseBody)
		require.True(t, gjson.GetBytes(shaped, "stream").Bool())
		require.True(t, gjson.GetBytes(shaped, "enable_thinking").Bool())
	})

	t.Run("qwen3.8 near miss keeps generic non-streaming shape", func(t *testing.T) {
		shaped := applyNewAPIResponsesChatFallbackShape("qwen3.8-32b", baseBody)
		require.False(t, gjson.GetBytes(shaped, "stream").Bool())
		require.False(t, gjson.GetBytes(shaped, "enable_thinking").Bool())
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

func TestApplyNewAPIQwenNonStreamingShape(t *testing.T) {
	t.Run("non-streaming qwen disables thinking", func(t *testing.T) {
		body := []byte(`{"model":"qwen3-8b","stream":false,"enable_thinking":true}`)
		shaped := applyNewAPIQwenNonStreamingShape("qwen3-8b", body)
		require.False(t, gjson.GetBytes(shaped, "enable_thinking").Bool())
	})

	t.Run("streaming qwen preserves thinking", func(t *testing.T) {
		body := []byte(`{"model":"qwen3-8b","stream":true,"enable_thinking":true}`)
		shaped := applyNewAPIQwenNonStreamingShape("qwen3-8b", body)
		require.True(t, gjson.GetBytes(shaped, "enable_thinking").Bool())
	})

	t.Run("other models remain unchanged", func(t *testing.T) {
		body := []byte(`{"model":"deepseek-chat","stream":false,"enable_thinking":true}`)
		shaped := applyNewAPIQwenNonStreamingShape("deepseek-chat", body)
		require.Equal(t, string(body), string(shaped))
	})

	t.Run("unrelated large integers retain precision", func(t *testing.T) {
		body := []byte(`{"model":"qwen3-8b","stream":false,"enable_thinking":true,"tools":[{"function":{"parameters":{"const":9007199254740993}}}]}`)
		shaped := applyNewAPIQwenNonStreamingShape("qwen3-8b", body)
		require.False(t, gjson.GetBytes(shaped, "enable_thinking").Bool())
		require.Equal(t, "9007199254740993", gjson.GetBytes(shaped, "tools.0.function.parameters.const").Raw)
	})
}

func forceChatResponsesFallbackAccount() *Account {
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
	}
	return account
}

// reasoningRecordingCache 记录 reasoning 缓存写入、并按需响应回查。
type reasoningRecordingCache struct {
	stubGatewayCache
	mu      sync.Mutex
	sets    map[string]string
	getResp map[string]string
}

func (c *reasoningRecordingCache) SetReasoningContent(_ context.Context, itemID string, content string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sets == nil {
		c.sets = make(map[string]string)
	}
	c.sets[itemID] = content
	return nil
}

func (c *reasoningRecordingCache) GetReasoningContent(_ context.Context, itemID string) (string, error) {
	if v, ok := c.getResp[itemID]; ok {
		return v, nil
	}
	return "", ErrReasoningContentNotFound
}

func (c *reasoningRecordingCache) snapshotSets() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.sets))
	for k, v := range c.sets {
		out[k] = v
	}
	return out
}

// 流式响应里的 reasoning_content 应按 reasoning item id 写入缓存，供后续轮次
// 客户端不回传明文 summary 时回注（DeepSeek thinking mode 400 修复的写入侧）。
func TestForwardResponses_ChatFallbackCachesStreamedReasoning(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"deepseek-reasoner","input":"hello","stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_rc","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_rc","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"reasoning_content":"think "},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_rc","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"reasoning_content":"first"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_rc","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
		"",
		`data: {"id":"chatcmpl_rc","object":"chat.completion.chunk","model":"deepseek-reasoner","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_reasoning_cache_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	cache := &reasoningRecordingCache{}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
		cache:        cache,
	}

	result, err := svc.Forward(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)

	sets := cache.snapshotSets()
	require.Len(t, sets, 1, "应恰好缓存一个 reasoning item")
	for itemID, content := range sets {
		require.NotEmpty(t, itemID)
		require.Equal(t, "think first", content)
	}
}

// 请求侧：encrypted-only reasoning item（无明文 summary）经缓存回查补回
// reasoning_content；带明文 summary 的 item 顺手回写缓存（自愈）。
func TestForwardResponses_ChatFallbackRestoresReasoningFromCache(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{
		"model":"deepseek-reasoner",
		"stream":false,
		"input":[
			{"type":"reasoning","id":"item_plain","summary":[{"type":"summary_text","text":"plain thinking"}]},
			{"type":"function_call","call_id":"call_0","name":"get_value","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_0","output":"ok"},
			{"type":"reasoning","id":"item_enc1","summary":[],"encrypted_content":"opaque"},
			{"type":"function_call","call_id":"call_1","name":"get_value","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}
		]
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_reasoning_cache_restore"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_restore","object":"chat.completion","model":"deepseek-reasoner","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		)),
	}}
	cache := &reasoningRecordingCache{
		getResp: map[string]string{"item_enc1": "cached thinking"},
	}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
		cache:        cache,
	}

	result, err := svc.Forward(context.Background(), c, forceChatResponsesFallbackAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)

	// 明文 summary 的 assistant 工具调用消息：reasoning_content 来自 summary 本身。
	require.Equal(t, "plain thinking", gjson.GetBytes(upstream.lastBody, "messages.0.reasoning_content").String())
	require.Equal(t, "call_0", gjson.GetBytes(upstream.lastBody, "messages.0.tool_calls.0.id").String())
	require.Equal(t, "tool", gjson.GetBytes(upstream.lastBody, "messages.1.role").String())
	// encrypted-only 的 assistant 工具调用消息：reasoning_content 来自缓存回查。
	require.Equal(t, "cached thinking", gjson.GetBytes(upstream.lastBody, "messages.2.reasoning_content").String())
	require.Equal(t, "call_1", gjson.GetBytes(upstream.lastBody, "messages.2.tool_calls.0.id").String())
	require.Equal(t, "tool", gjson.GetBytes(upstream.lastBody, "messages.3.role").String())

	// 明文 summary 的 item 被回写进缓存（自愈）。
	require.Equal(t, "plain thinking", cache.snapshotSets()["item_plain"])
}

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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func tokenseaNativeMessagesAccount() *Account {
	account := rawChatCompletionsTestAccount()
	account.Credentials["base_url"] = "https://agent.tokensea.ai"
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesSupported:      false,
		openai_compat.ExtraKeyNativeMessagesSupported: true,
	}
	return account
}

func TestNativeMessagesUsageKeepsCacheOutOfUncachedInput(t *testing.T) {
	for _, stream := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		body := `{"type":"message","usage":{"input_tokens":100,"cache_read_input_tokens":50,"output_tokens":4}}`
		if stream {
			body = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":50,\"output_tokens\":0}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":4}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
		}
		resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
		var result *OpenAIForwardResult
		var err error
		if stream {
			result, err = svc.streamNativeAnthropicMessages(c, resp, tokenseaNativeMessagesAccount(), "composer-2.5", "composer-2.5", "composer-2.5", time.Now())
		} else {
			result, err = svc.bufferNativeAnthropicMessages(c, resp, "composer-2.5", "composer-2.5", "composer-2.5", time.Now())
		}
		require.NoError(t, err)
		require.Equal(t, 150, result.Usage.InputTokens)
		require.Equal(t, 50, result.Usage.CacheReadInputTokens)
		require.Equal(t, 4, result.Usage.OutputTokens)
	}
}

func TestForwardAsAnthropic_NativeMessagesPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"message","model":"claude-haiku-4-5-20251001","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":7,"output_tokens":1}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, tokenseaNativeMessagesAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://agent.tokensea.ai/v1/messages", upstream.lastReq.URL.String())
	require.Equal(t, "claude-haiku-4-5-20251001", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, rec.Body.String(), "OK")
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 1, result.Usage.OutputTokens)
}

// TestForwardAsAnthropic_NativeMessages_TokenseaFableStripsContextManagement pins
// the prod 2026-09-20 user16 path: ForwardAsAnthropic → forwardAnthropicViaNativeMessages
// → sendNativeAnthropicMessagesRequest must strip context_management for
// tokensea+fable before the upstream POST (this path does not use
// buildNativeAnthropicUpstreamRequest).
func TestForwardAsAnthropic_NativeMessages_TokenseaFableStripsContextManagement(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-fable-5","max_tokens":8,"thinking":{"type":"adaptive"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"user","content":"hi"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("anthropic-beta", "context-management-2025-06-27")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"message","model":"claude-fable-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	_, err := svc.ForwardAsAnthropic(context.Background(), c, tokenseaNativeMessagesAccount(), body, "", "")
	require.NoError(t, err)
	require.Equal(t, "https://agent.tokensea.ai/v1/messages", upstream.lastReq.URL.String())
	require.Equal(t, "claude-fable-5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "context_management").Exists(),
		"native messages egress must strip context_management for tokensea+fable")
	require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
}

// TestForwardAsAnthropic_NativeMessages_PrefiltersToolStormThinking pins the
// prod 2026-09-25 user16 path: native messages missed ToolSearch/tool-storm
// historical thinking prefilter and returned final 400 cannot-be-modified.
func TestForwardAsAnthropic_NativeMessages_PrefiltersToolStormThinking(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{
		"model":"claude-fable-5",
		"max_tokens":64,
		"thinking":{"type":"adaptive"},
		"tools":[{"name":"Bash"}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"task"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"historical","signature":"EuAG_stale"},
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}
		]
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"message","model":"claude-fable-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	_, err := svc.ForwardAsAnthropic(context.Background(), c, tokenseaNativeMessagesAccount(), body, "", "")
	require.NoError(t, err)
	require.Len(t, upstream.bodies, 1)

	var thinkingCount int
	for _, b := range gjson.GetBytes(upstream.lastBody, "messages.1.content").Array() {
		if b.Get("type").String() == "thinking" {
			thinkingCount++
		}
	}
	require.Equal(t, 0, thinkingCount, "native messages must strip historical signed thinking before first hop")
}

// TestForwardAsAnthropic_NativeMessages_ThinkingContract400Retry verifies the
// shared rectifier fires on the native path when prefilter still misses a case.
func TestForwardAsAnthropic_NativeMessages_ThinkingContract400Retry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Prefill-only signed thinking + tool_use: gate does not strip (no
	// historical), so first hop can still 400 cannot-be-modified; rectifier
	// escalates to signature-sensitive.
	body := []byte(`{
		"model":"claude-fable-5",
		"max_tokens":64,
		"thinking":{"type":"adaptive"},
		"tools":[{"name":"Bash"}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"latest","signature":"sig"},
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}
			]}
		]
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	err400 := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"error","error":{"type":"invalid_request_error","message":"thinking blocks in the latest assistant message cannot be modified"}}`,
		)),
	}
	ok200 := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"message","model":"claude-fable-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}`,
		)),
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{err400, ok200}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	_, err := svc.ForwardAsAnthropic(context.Background(), c, tokenseaNativeMessagesAccount(), body, "", "")
	require.NoError(t, err)
	require.Len(t, upstream.bodies, 2)

	var toolUse int
	for _, b := range gjson.GetBytes(upstream.bodies[1], "messages.1.content").Array() {
		if b.Get("type").String() == "tool_use" {
			toolUse++
		}
	}
	require.Equal(t, 0, toolUse, "400 retry must escalate to signature-sensitive tool downgrade")
}

func TestForwardAsAnthropic_NativeMessagesPreferredOverChatFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	account := tokenseaNativeMessagesAccount()
	account.Extra[openai_compat.ExtraKeyNativeMessagesSupported] = true
	account.Extra[openai_compat.ExtraKeyResponsesSupported] = false

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"message","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotContains(t, upstream.lastReq.URL.String(), "chat/completions")
	require.Contains(t, upstream.lastReq.URL.String(), "/v1/messages")
}

func tokenseaOpenAIRelayWithStaleNativeFlag() *Account {
	account := rawChatCompletionsTestAccount()
	account.Credentials["base_url"] = "https://agent.tokensea.ai"
	account.Extra = map[string]any{
		// Live 2026-08-20 account 92: GPT /v1/messages probe timed out, so
		// extra.openai_native_messages_supported=false while
		// openai_responses_supported=true. Direct upstream Claude
		// /v1/messages still returned 200.
		openai_compat.ExtraKeyResponsesSupported:      true,
		openai_compat.ExtraKeyNativeMessagesSupported: false,
	}
	return account
}

func TestForwardAsAnthropic_TokenseaClaudeUsesNativeMessagesEvenWhenExtraFlagFalse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"message","model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":7,"output_tokens":1}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, tokenseaOpenAIRelayWithStaleNativeFlag(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://agent.tokensea.ai/v1/messages", upstream.lastReq.URL.String(),
		"92 Claude /v1/messages must passthrough even when extra.native_messages=false")
	require.NotContains(t, upstream.lastReq.URL.String(), "/responses")
	require.Contains(t, rec.Body.String(), "OK")
}

func TestForwardAsAnthropic_TokenseaGPTKeepsResponsesWhenFlagTrue(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_native","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, tokenseaOpenAIRelayWithStaleNativeFlag(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, "/responses"),
		"92 GPT /v1/messages must stay on Responses when extra.responses=true, got %s", upstream.lastReq.URL.String())
}

type delayingHTTPUpstream struct {
	delay time.Duration
	inner HTTPUpstream
}

func (u *delayingHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	time.Sleep(u.delay)
	return u.inner.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *delayingHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestForwardAsAnthropic_HeaderWaitKeepaliveEmitsAnthropicPing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_ka","object":"response","model":"gpt-5.4","status":"in_progress"}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_ka","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	inner := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	cfg := rawChatCompletionsTestConfig()
	cfg.Gateway.StreamKeepaliveInterval = 1
	svc := &OpenAIGatewayService{
		cfg:          cfg,
		httpUpstream: &delayingHTTPUpstream{delay: 1100 * time.Millisecond, inner: inner},
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	bodyOut := rec.Body.String()
	require.Contains(t, bodyOut, "event: ping",
		"Claude /v1/messages clients must receive Anthropic ping while waiting on upstream headers")
	require.Contains(t, bodyOut, "event: message_start")
	require.Less(t, strings.Index(bodyOut, "event: message_start"), strings.Index(bodyOut, "event: ping"),
		"message_start must precede the first ping so Claude Code keeps the late-first-token stream")
	require.Contains(t, bodyOut, `"text_delta"`)
	require.Contains(t, bodyOut, "ok")
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestForwardAsAnthropic_JSONResponsesBodyStreamsVisibleText(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstreamJSON := `{
		"id":"resp_json","object":"response","model":"gpt-5.4","status":"completed",
		"output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"from json"}]}],
		"usage":{"input_tokens":11,"output_tokens":4,"total_tokens":15}
	}`
	svc := &OpenAIGatewayService{
		cfg: rawChatCompletionsTestConfig(),
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamJSON)),
		}},
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), "event: message_start")
	require.Contains(t, rec.Body.String(), "from json")
}

func TestForwardAsAnthropic_JSONNonResponsesBodyIsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	svc := &OpenAIGatewayService{
		cfg: rawChatCompletionsTestConfig(),
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}},
	}

	_, err := svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), body, "", "")
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
}

func TestForwardAsAnthropic_JSONFailedResponsesIsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstreamJSON := `{
		"id":"resp_failed","object":"response","model":"gpt-5.4","status":"failed",
		"output":[],
		"error":{"code":"invalid_request_error","message":"messages is not allowed for this model"},
		"usage":{"input_tokens":3,"output_tokens":0}
	}`
	svc := &OpenAIGatewayService{
		cfg: rawChatCompletionsTestConfig(),
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamJSON)),
		}},
	}

	_, err := svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), body, "", "")
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover), "non-retryable failed JSON must not failover")
	require.NotContains(t, rec.Body.String(), "event: message_stop")
	require.NotContains(t, rec.Body.String(), `"stop_reason":"end_turn"`)
}

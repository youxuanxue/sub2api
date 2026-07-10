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
	"github.com/tidwall/gjson"
)

func TestTkStripDeprecatedSamplingParams_StripIsTopLevelOnly(t *testing.T) {
	input := []byte(`{"model":"claude-opus-4-7","temperature":0.7,"top_p":0.9,"top_k":40,"tools":[{"name":"set_temp","input_schema":{"type":"object","properties":{"temperature":{"type":"number"},"top_p":{"type":"number"},"top_k":{"type":"integer"}}}}],"messages":[{"role":"user","content":"hi"}]}`)

	got := tkStripDeprecatedSamplingParams(input)

	require.False(t, gjson.GetBytes(got, "temperature").Exists())
	require.False(t, gjson.GetBytes(got, "top_p").Exists())
	require.False(t, gjson.GetBytes(got, "top_k").Exists())
	require.True(t, gjson.GetBytes(got, "tools.0.input_schema.properties.temperature").Exists())
	require.True(t, gjson.GetBytes(got, "tools.0.input_schema.properties.top_p").Exists())
	require.True(t, gjson.GetBytes(got, "tools.0.input_schema.properties.top_k").Exists())
	require.Equal(t, "claude-opus-4-7", gjson.GetBytes(got, "model").String())
	require.True(t, gjson.ValidBytes(got))
}

func TestTkStripDeprecatedSamplingParams_NoTouchForOlderClaudeModels(t *testing.T) {
	cases := []string{
		`{"model":"claude-opus-4-6","top_p":0.9,"top_k":40,"messages":[]}`,
		`{"model":"claude-haiku-4-5","top_p":0.9,"top_k":40,"messages":[]}`,
		`{"model":"gpt-5.5","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[]}`,
	}
	for _, body := range cases {
		t.Run(gjson.Get(body, "model").String(), func(t *testing.T) {
			got := tkStripDeprecatedSamplingParams([]byte(body))
			require.Equal(t, body, string(got))
		})
	}
}

func TestTkStripDeprecatedSamplingParams_StripsTopPWhenTemperatureAlsoPresent(t *testing.T) {
	input := []byte(`{"model":"claude-sonnet-4-5","temperature":0.7,"top_p":0.9,"top_k":40,"tools":[{"name":"set_temp","input_schema":{"type":"object","properties":{"top_p":{"type":"number"}}}}],"messages":[{"role":"user","content":"hi"}]}`)

	got := tkStripDeprecatedSamplingParams(input)

	require.True(t, gjson.GetBytes(got, "temperature").Exists())
	require.False(t, gjson.GetBytes(got, "top_p").Exists())
	require.True(t, gjson.GetBytes(got, "top_k").Exists())
	require.True(t, gjson.GetBytes(got, "tools.0.input_schema.properties.top_p").Exists())
	require.Equal(t, "claude-sonnet-4-5", gjson.GetBytes(got, "model").String())
	require.True(t, gjson.ValidBytes(got))
}

func TestTkStripDeprecatedSamplingParams_StripsTopPForSonnet46(t *testing.T) {
	input := []byte(`{"model":"claude-sonnet-4-6","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[{"role":"user","content":"hi"}]}`)

	got := tkStripDeprecatedSamplingParams(input)

	require.True(t, gjson.GetBytes(got, "temperature").Exists())
	require.False(t, gjson.GetBytes(got, "top_p").Exists())
	require.True(t, gjson.GetBytes(got, "top_k").Exists())
	require.Equal(t, "claude-sonnet-4-6", gjson.GetBytes(got, "model").String())
	require.True(t, gjson.ValidBytes(got))
}

func TestTkStripDeprecatedSamplingParams_StripsTopPForOpus41(t *testing.T) {
	input := []byte(`{"model":"claude-opus-4-1-20250805","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[{"role":"user","content":"hi"}]}`)

	got := tkStripDeprecatedSamplingParams(input)

	require.True(t, gjson.GetBytes(got, "temperature").Exists())
	require.False(t, gjson.GetBytes(got, "top_p").Exists())
	require.True(t, gjson.GetBytes(got, "top_k").Exists())
	require.Equal(t, "claude-opus-4-1-20250805", gjson.GetBytes(got, "model").String())
	require.True(t, gjson.ValidBytes(got))
}

func TestTkStripDeprecatedSamplingParams_StripsTopPForDatedHaiku45(t *testing.T) {
	input := []byte(`{"model":"claude-haiku-4-5-20251001","temperature":1,"top_p":0.999,"messages":[{"role":"user","content":"hi"}]}`)

	got := tkStripDeprecatedSamplingParams(input)

	require.True(t, gjson.GetBytes(got, "temperature").Exists())
	require.False(t, gjson.GetBytes(got, "top_p").Exists())
	require.Equal(t, "claude-haiku-4-5-20251001", gjson.GetBytes(got, "model").String())
	require.True(t, gjson.ValidBytes(got))
}

func TestTkStripDeprecatedSamplingParams_DoesNotStripSingleSamplingParamFor45(t *testing.T) {
	cases := []string{
		`{"model":"claude-sonnet-4-5","temperature":0.7,"messages":[]}`,
		`{"model":"claude-haiku-4-5","top_p":0.9,"messages":[]}`,
		`{"model":"claude-sonnet-4-6","temperature":0.7,"messages":[]}`,
		`{"model":"claude-sonnet-4-6","top_p":0.9,"messages":[]}`,
		`{"model":"claude-sonnet-4-6","top_k":40,"messages":[]}`,
	}
	for _, body := range cases {
		t.Run(gjson.Get(body, "model").String(), func(t *testing.T) {
			got := tkStripDeprecatedSamplingParams([]byte(body))
			require.Equal(t, body, string(got))
		})
	}
}

func TestTkModelDeprecatesSamplingParams(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-opus-4-7", true},
		{"claude-opus-4.8", true},
		{"anthropic.claude-opus-5-v1:0", true},
		{"claude-sonnet-4-6", false},
		{"claude-sonnet-4-6-20260708", false},
		{"claude-sonnet-5", true},
		{"claude-sonnet-5-20260708", true},
		{"anthropic.claude-sonnet-5-v1:0", true},
		{"claude-opus-4-6", false},
		{"claude-sonnet-4-5", false},
		{"claude-haiku-5-0", false},
		{"gpt-5.5", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			require.Equal(t, tc.want, tkModelDeprecatesSamplingParams(tc.model))
		})
	}
}

func TestTkModelRejectsTemperatureTopPCombination(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-4-5", true},
		{"claude-sonnet-4-5-20250929", true},
		{"claude-haiku-4-5", true},
		{"claude-haiku-4-5-20251001", true},
		{"claude-opus-4-1", true},
		{"claude-opus-4-1-20250805", true},
		{"anthropic.claude-opus-4-5-v1:0", true},
		{"claude-opus-4-6", true},
		{"claude-opus-4-7", false},
		{"claude-sonnet-4-6", true},
		{"claude-sonnet-4-6-20260708", true},
		{"claude-sonnet-5", false},
		{"claude-3-5-sonnet-20241022", false},
		{"gpt-5.5", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			require.Equal(t, tc.want, tkModelRejectsTemperatureTopPCombination(tc.model))
		})
	}
}

func TestTkSamplingParamRuleFromAnthropic400(t *testing.T) {
	conflictBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"` + "`temperature` and `top_p` cannot both be specified for this model. Please use only one." + `"}}`)
	rule, ok := tkSamplingParamRuleFromAnthropic400("claude-sonnet-4-5", http.StatusBadRequest, conflictBody)
	require.True(t, ok)
	require.Equal(t, tkSamplingParamRuleStripTopPWithTemperature, rule)

	unsupportedBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"temperature: Extra inputs are not permitted"}}`)
	rule, ok = tkSamplingParamRuleFromAnthropic400("claude-next", http.StatusBadRequest, unsupportedBody)
	require.True(t, ok)
	require.Equal(t, tkSamplingParamRuleStripAll, rule)

	unknownParamBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Unknown parameter: top_k"}}`)
	rule, ok = tkSamplingParamRuleFromAnthropic400("claude-next", http.StatusBadRequest, unknownParamBody)
	require.True(t, ok)
	require.Equal(t, tkSamplingParamRuleStripAll, rule)

	unrecognizedParamBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Unrecognized request argument supplied: top_p"}}`)
	rule, ok = tkSamplingParamRuleFromAnthropic400("claude-next", http.StatusBadRequest, unrecognizedParamBody)
	require.True(t, ok)
	require.Equal(t, tkSamplingParamRuleStripAll, rule)

	nonSamplingUnknownParamBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Unknown parameter: max_tokens"}}`)
	_, ok = tkSamplingParamRuleFromAnthropic400("claude-next", http.StatusBadRequest, nonSamplingUnknownParamBody)
	require.False(t, ok)

	overloadedBody := []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	_, ok = tkSamplingParamRuleFromAnthropic400("claude-next", http.StatusBadRequest, overloadedBody)
	require.False(t, ok)

	rangeBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"temperature: Input should be less than or equal to 1"}}`)
	_, ok = tkSamplingParamRuleFromAnthropic400("claude-next", http.StatusBadRequest, rangeBody)
	require.False(t, ok)

	prefillBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Prefilling assistant messages is not supported for this model."}}`)
	_, ok = tkSamplingParamRuleFromAnthropic400("claude-opus-4-8", http.StatusBadRequest, prefillBody)
	require.False(t, ok)

	thinkingBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: thinking or redacted_thinking blocks in the latest assistant message cannot be modified."}}`)
	_, ok = tkSamplingParamRuleFromAnthropic400("claude-sonnet-4-5", http.StatusBadRequest, thinkingBody)
	require.False(t, ok)
}

func TestTkAnthropicThinkingRuleFrom400(t *testing.T) {
	adaptiveBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"\"thinking.type.enabled\" is not supported for this model. Use \"thinking.type.adaptive\" and \"output_config.effort\" to control thinking behavior."}}`)
	rule, ok := tkAnthropicThinkingRuleFrom400("claude-next-preview", http.StatusBadRequest, adaptiveBody)
	require.True(t, ok)
	require.Equal(t, tkAnthropicThinkingRuleAdaptiveOnly, rule)

	budgetBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"thinking.budget_tokens: Input should be greater than or equal to 1024"}}`)
	_, ok = tkAnthropicThinkingRuleFrom400("claude-sonnet-4-5", http.StatusBadRequest, budgetBody)
	require.False(t, ok)

	prefillBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Prefilling assistant messages is not supported for this model."}}`)
	_, ok = tkAnthropicThinkingRuleFrom400("claude-opus-4-8", http.StatusBadRequest, prefillBody)
	require.False(t, ok)

	signatureBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid signature in thinking block"}}`)
	_, ok = tkAnthropicThinkingRuleFrom400("claude-sonnet-4-5", http.StatusBadRequest, signatureBody)
	require.False(t, ok)

	overloadedBody := []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	_, ok = tkAnthropicThinkingRuleFrom400("claude-next-preview", http.StatusBadRequest, overloadedBody)
	require.False(t, ok)

	_, ok = tkAnthropicThinkingRuleFrom400("claude-next-preview", http.StatusTooManyRequests, adaptiveBody)
	require.False(t, ok)
}

func TestTkStripDeprecatedSamplingParams_UsesCachedSamplingRule(t *testing.T) {
	tkSamplingParamRules.Flush()
	defer tkSamplingParamRules.Flush()

	body := []byte(`{"model":"claude-next-preview","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[]}`)
	require.Equal(t, body, tkStripDeprecatedSamplingParams(body))

	tkPutCachedSamplingParamRule("claude-next-preview", tkSamplingParamRuleStripTopPWithTemperature)
	got := tkStripDeprecatedSamplingParams(body)
	require.True(t, gjson.GetBytes(got, "temperature").Exists())
	require.False(t, gjson.GetBytes(got, "top_p").Exists())
	require.True(t, gjson.GetBytes(got, "top_k").Exists())
}

func TestTkApplyAnthropicRequestCompatibilityRules_UsesCachedAdaptiveThinkingRule(t *testing.T) {
	tkAnthropicThinkingRules.Flush()
	defer tkAnthropicThinkingRules.Flush()

	body := []byte(`{"model":"claude-next-preview","thinking":{"type":"enabled","budget_tokens":1024},"max_tokens":2048,"messages":[]}`)
	require.Equal(t, body, tkApplyAnthropicRequestCompatibilityRules(body))

	tkPutCachedAnthropicThinkingRule("claude-next-preview", tkAnthropicThinkingRuleAdaptiveOnly)
	got := tkApplyAnthropicRequestCompatibilityRules(body)
	require.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String())
	require.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
	require.Equal(t, "claude-next-preview", gjson.GetBytes(got, "model").String())
}

func TestRectifyAnthropicPassthrough400_SamplingParamRetryCachesRule(t *testing.T) {
	tkSamplingParamRules.Flush()
	defer tkSamplingParamRules.Flush()

	body := []byte(`{"model":"claude-next-preview","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[]}`)
	respBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"You should either alter temperature or top_p, but not both."}}`)
	svc := &GatewayService{}
	account := &Account{Platform: PlatformAnthropic}

	got, kind, ok := svc.rectifyAnthropicPassthrough400(context.Background(), account, body, "claude-next-preview", respBody)

	require.True(t, ok)
	require.Equal(t, "sampling_param_retry", kind)
	require.True(t, gjson.GetBytes(got, "temperature").Exists())
	require.False(t, gjson.GetBytes(got, "top_p").Exists())
	require.True(t, gjson.GetBytes(got, "top_k").Exists())
	rule, exists := tkGetCachedSamplingParamRule("claude-next-preview")
	require.True(t, exists)
	require.Equal(t, tkSamplingParamRuleStripTopPWithTemperature, rule)
}

func TestForwardAsChatCompletions_AnthropicOpus47StripsDeprecatedSamplingParams(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-opus-4-7","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-7","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_opus47_sampling"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &GatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          52,
		Name:        "cc-us3",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-ant-relay",
		},
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists(),
		"Opus 4.7+ rejects top-level temperature on Anthropic Messages")
	require.False(t, gjson.GetBytes(upstream.lastBody, "top_p").Exists(),
		"Opus 4.7+ rejects top-level top_p on Anthropic Messages")
	require.False(t, gjson.GetBytes(upstream.lastBody, "top_k").Exists(),
		"Opus 4.7+ rejects top-level top_k on Anthropic Messages")
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
	require.Equal(t, "claude-opus-4-7", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestForwardAsChatCompletions_AnthropicSonnet45StripsTopPConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4-5","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_sonnet45_sampling"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &GatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          54,
		Name:        "cc-us4",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-ant-relay",
		},
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "top_p").Exists(),
		"Claude 4.5 rejects temperature and top_p in the same Anthropic Messages request")
	require.Equal(t, "claude-sonnet-4-5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestApplyClaudeCodeOAuthMimicry_AnthropicOpus47StripsDeprecatedSamplingParams(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[{"role":"user","content":"hello"}]}`)
	svc := &GatewayService{}
	account := &Account{
		ID:       53,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	got := svc.applyClaudeCodeOAuthMimicryToBody(context.Background(), nil, account, body, nil, "claude-opus-4-7")

	require.False(t, gjson.GetBytes(got, "temperature").Exists(),
		"OAuth mimicry normalize must not leave deprecated top-level temperature for Opus 4.7+")
	require.False(t, gjson.GetBytes(got, "top_p").Exists(),
		"OAuth mimicry normalize must not leave deprecated top-level top_p for Opus 4.7+")
	require.False(t, gjson.GetBytes(got, "top_k").Exists(),
		"OAuth mimicry normalize must not leave deprecated top-level top_k for Opus 4.7+")
	require.True(t, gjson.GetBytes(got, "messages").Exists())
	require.Equal(t, "claude-opus-4-7", gjson.GetBytes(got, "model").String())
}

func TestApplyClaudeCodeOAuthMimicry_AnthropicHaiku45StripsTopPConflict(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","temperature":0.7,"top_p":0.9,"top_k":40,"messages":[{"role":"user","content":"hello"}]}`)
	svc := &GatewayService{}
	account := &Account{
		ID:       55,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	got := svc.applyClaudeCodeOAuthMimicryToBody(context.Background(), nil, account, body, nil, "claude-haiku-4-5")

	require.True(t, gjson.GetBytes(got, "temperature").Exists())
	require.False(t, gjson.GetBytes(got, "top_p").Exists(),
		"OAuth mimicry normalize must not leave top_p next to temperature for Haiku 4.5")
	require.True(t, gjson.GetBytes(got, "top_k").Exists())
	require.True(t, gjson.GetBytes(got, "messages").Exists())
	require.Equal(t, "claude-haiku-4-5-20251001", gjson.GetBytes(got, "model").String())
}

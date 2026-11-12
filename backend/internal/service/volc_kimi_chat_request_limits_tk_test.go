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

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 2026-11-04 cn-prod kimi-k3 benchmark: these requests returned 200 while the
// supplier silently ignored or reinterpreted the field.
func TestVolcKimiRequestLimitsRejectUndeliverableFields(t *testing.T) {
	account := volcEnginePlanTestAccount()
	for _, tc := range []struct {
		field, param string
	}{
		{`"top_p":-0.5`, "top_p"},
		{`"top_p":1.5`, "top_p"},
		{`"top_p":"0.9"`, "top_p"},
		{`"temperature":-0.1`, "temperature"},
		{`"temperature":2.1`, "temperature"},
		{`"temperature":"hot"`, "temperature"},
		{`"n":2`, "n"},
		{`"n":0`, "n"},
		{`"n":1.5`, "n"},
		{`"logprobs":true`, "logprobs"},
		{`"top_logprobs":2`, "top_logprobs"},
		{`"logprobs":true,"top_logprobs":2`, "logprobs"},
		{`"max_tokens":0`, "max_tokens"},
		{`"max_tokens":-1`, "max_tokens"},
		{`"max_tokens":1.5`, "max_tokens"},
		{`"max_completion_tokens":0`, "max_completion_tokens"},
		// 2026-11-05 direct control: Volc returned 200 for every prediction shape.
		{`"prediction":"OK"`, "prediction"},
		{`"prediction":{"type":"bogus","content":"OK"}`, "prediction.type"},
		{`"prediction":{"content":"OK"}`, "prediction.type"},
		{`"prediction":{"type":"content","content":123}`, "prediction.content"},
		{`"prediction":{"type":"content"}`, "prediction.content"},
		{`"prediction":{"type":"content","content":[{"type":"image_url","image_url":{"url":"x"}}]}`, "prediction.content"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],` + tc.field + `}`)
			_, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", body)
			var invalid *OpenAIInvalidParameterError
			require.ErrorAs(t, err, &invalid)
			require.Equal(t, tc.param, invalid.Param)
			require.Equal(t, "invalid_parameter", invalid.Code)
			require.NotContains(t, strings.ToLower(invalid.Message), "volc")
		})
	}
	for _, field := range []string{
		`"top_p":0`, `"top_p":1`, `"top_p":0.95`, `"temperature":0`, `"temperature":1`, `"temperature":2`,
		`"n":1`, `"logprobs":false`, `"top_logprobs":0`, `"max_tokens":1`, `"max_completion_tokens":131072`,
		`"top_p":null`, `"temperature":null`, `"n":null`, `"logprobs":null`, `"max_tokens":null`,
		`"prediction":{"type":"content","content":"OK"}`,
		`"prediction":{"type":"content","content":[{"type":"text","text":"OK"}]}`, `"prediction":null`,
	} {
		t.Run("ok/"+field, func(t *testing.T) {
			body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],` + field + `}`)
			got, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", body)
			require.NoError(t, err)
			require.JSONEq(t, string(body), string(got), "valid values must be forwarded unchanged")
		})
	}
	// Supplier scope: other Kimi routes keep their existing behavior.
	other, err := applyMoonshotThinkingShape(nil, nil, "kimi-k3", []byte(`{"n":2,"top_p":1.5}`))
	require.NoError(t, err)
	require.EqualValues(t, 2, gjson.GetBytes(other, "n").Int())

	// kimi-k3.1-pro is the same Volc K3 family. n=2 is rejected; an in-range
	// temperature is forwarded and is not rewritten to 1.
	_, err = applyMoonshotThinkingShape(nil, account, "kimi-k3.1-pro", []byte(`{"model":"kimi-k3.1-pro","n":2}`))
	var invalid *OpenAIInvalidParameterError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, "n", invalid.Param)
	forwarded, err := applyMoonshotThinkingShape(nil, account, "kimi-k3.1-pro", []byte(`{"model":"kimi-k3.1-pro","temperature":0.6,"messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	require.InDelta(t, 0.6, gjson.GetBytes(forwarded, "temperature").Float(), 0.0001)
}

func TestVolcKimiReasoningEffortNoneDisablesThinking(t *testing.T) {
	account := volcEnginePlanTestAccount()
	for _, in := range []string{
		`{"reasoning_effort":"none"}`,
		`{"reasoning_effort":"NONE"}`,
		`{"reasoning_effort":"off"}`,
		`{"reasoning":{"effort":"none"}}`,
		`{"thinking":{"type":"disabled"},"reasoning_effort":"none"}`,
		`{"thinking":{"type":""},"reasoning_effort":"none"}`,
	} {
		t.Run(in, func(t *testing.T) {
			got, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", []byte(in))
			require.NoError(t, err)
			require.Equal(t, "disabled", gjson.GetBytes(got, "thinking.type").String())
			require.False(t, gjson.GetBytes(got, "reasoning_effort").Exists())
			require.False(t, gjson.GetBytes(got, "reasoning").Exists())
			again, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", got)
			require.NoError(t, err)
			require.JSONEq(t, string(got), string(again))
		})
	}
	for _, in := range []string{
		`{"thinking":{"type":"enabled"},"reasoning_effort":"none"}`,
		`{"enable_thinking":true,"reasoning_effort":"none"}`,
	} {
		_, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", []byte(in))
		var invalid *OpenAIInvalidParameterError
		require.ErrorAs(t, err, &invalid, in)
		require.Equal(t, "reasoning_effort", invalid.Param)
	}
	// minimal is still a low-effort request, not an off-switch.
	minimal, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", []byte(`{"reasoning_effort":"minimal"}`))
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(minimal, "reasoning_effort").String())
	require.Equal(t, "enabled", gjson.GetBytes(minimal, "thinking.type").String())
}

// The raw Chat route must stop before relay and return the local field 400.
func TestVolcKimiRequestLimitsForwardReturnsLocal400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := volcAgentPlanKimiAccountForTest()
	body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"n":2}`)
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	_, err := svc.ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", "")
	var invalid *OpenAIInvalidParameterError
	require.ErrorAs(t, err, &invalid)
	require.Nil(t, upstream.lastReq, "invalid request must not reach the supplier")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "n", gjson.Get(rec.Body.String(), "error.param").String())
	require.Equal(t, "invalid_parameter", gjson.Get(rec.Body.String(), "error.code").String())
}

const volcKimiUpstreamRequestIDMessage = "The parameter `max_tokens` specified in the request are not valid: must be positive. Request id: 021793796431474c0f0a8de5025091839561e6fd538adcb9e5394"

func TestTkSanitizeUpstreamProductWireMessageStripsSupplierRequestID(t *testing.T) {
	public, sanitized := TkSanitizeUpstreamProductWireMessage(volcKimiUpstreamRequestIDMessage)
	require.False(t, sanitized, "request id trimming is not a product rewrite")
	require.Equal(t, "The parameter `max_tokens` specified in the request are not valid: must be positive.", public)
	// Unrelated text and other relays' request id formats are untouched.
	for _, keep := range []string{
		"用户额度不足 (request id: 202608301312116953332028268d9d6pZbBrFFG)",
		"Request id is required",
		"see Request id: abc in docs",
	} {
		got, _ := TkSanitizeUpstreamProductWireMessage(keep)
		require.Equal(t, keep, got)
	}
}

func TestVolcKimiUpstream400HidesSupplierRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := volcAgentPlanKimiAccountForTest()
	body := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}]}`)
	errBody := `{"error":{"code":"InvalidParameter","message":"` + strings.ReplaceAll(volcKimiUpstreamRequestIDMessage, "`", "\\u0060") + `","param":"max_tokens","type":"BadRequest"}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(errBody))}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	_, err := svc.ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", "")
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	msg := gjson.Get(rec.Body.String(), "error.message").String()
	require.Contains(t, msg, "must be positive")
	require.NotContains(t, msg, "Request id")
	require.NotContains(t, rec.Body.String(), "021793796431474c")
	opsMsg, _ := c.Get(OpsUpstreamErrorMessageKey)
	require.Contains(t, opsMsg, "Request id: 021793796431474c", "ops keeps the supplier request id")
}

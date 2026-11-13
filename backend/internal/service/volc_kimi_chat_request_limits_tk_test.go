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
		{`"temperature":1.1`, "temperature"},
		{`"temperature":2`, "temperature"},
		{`"temperature":2.0`, "temperature"},
		{`"temperature":2.1`, "temperature"},
		{`"temperature":"hot"`, "temperature"},
		{`"top_p":0`, "top_p"},
		{`"top_p":0.8`, "top_p"},
		{`"top_p":1`, "top_p"},
		{`"presence_penalty":0.5`, "presence_penalty"},
		{`"frequency_penalty":0.5`, "frequency_penalty"},
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
		`"top_p":0.95`, `"temperature":0`, `"temperature":0.6`, `"temperature":1`,
		`"presence_penalty":0`, `"frequency_penalty":0`,
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
	// Public kimi-k3 rejects KVV-illegal sampling even when the account is not
	// the supplier that owns the rest of the normalizer. top_p is checked first.
	_, err := applyMoonshotThinkingShape(nil, nil, "kimi-k3", []byte(`{"n":2,"top_p":1.5}`))
	var otherInvalid *OpenAIInvalidParameterError
	require.ErrorAs(t, err, &otherInvalid)
	require.Equal(t, "top_p", otherInvalid.Param)
	_, err = applyMoonshotThinkingShape(nil, nil, "kimi-k3", []byte(`{"model":"kimi-k3","n":2}`))
	require.ErrorAs(t, err, &otherInvalid)
	require.Equal(t, "n", otherInvalid.Param)

	// kimi-k3.1-pro and other kimi-* ids use the same KVV table. n=2, temperature
	// 1.1 and 2.0 are rejected. A legal temperature stays client-authored.
	_, err = applyMoonshotThinkingShape(nil, account, "kimi-k3.1-pro", []byte(`{"model":"kimi-k3.1-pro","n":2}`))
	var invalid *OpenAIInvalidParameterError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, "n", invalid.Param)
	for _, model := range []string{"kimi-k3.1-pro", "kimi-k2.5", "kimi-k30"} {
		for _, raw := range []string{`"temperature":1.1`, `"temperature":2.0`, `"top_p":0.8`, `"n":2`} {
			_, ferr := applyMoonshotThinkingShape(nil, nil, model, []byte(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],`+raw+`}`))
			var rejected *OpenAIInvalidParameterError
			require.ErrorAs(t, ferr, &rejected)
		}
		forwarded, ferr := applyMoonshotThinkingShape(nil, nil, model, []byte(`{"model":"`+model+`","temperature":0.6,"messages":[{"role":"user","content":"hi"}]}`))
		require.NoError(t, ferr)
		require.InDelta(t, 0.6, gjson.GetBytes(forwarded, "temperature").Float(), 0.0001)
		_, ferr = applyMoonshotThinkingShape(nil, nil, model, []byte(`{"model":"`+model+`","messages":[{"role":"tool","content":"result"},{"role":"user","content":"hi"}]}`))
		var orphan *OpenAIInvalidParameterError
		require.ErrorAs(t, ferr, &orphan)
		require.Equal(t, "messages", orphan.Param)
	}
}

func TestVolcKimiFixedSamplingSurvivesAliPin(t *testing.T) {
	account := volcEnginePlanTestAccount()
	illegal := []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"temperature":1.1,"top_p":0.8,"n":2,"presence_penalty":0.5,"frequency_penalty":0.5}`)
	pinned := applyNewAPIAliFixedSamplingShape("kimi-k3", illegal)
	require.InDelta(t, 1.1, gjson.GetBytes(pinned, "temperature").Float(), kimiK3FixedSamplingEpsilon)
	require.InDelta(t, 0.8, gjson.GetBytes(pinned, "top_p").Float(), kimiK3FixedSamplingEpsilon)
	require.EqualValues(t, 2, gjson.GetBytes(pinned, "n").Int())
	require.InDelta(t, 0.5, gjson.GetBytes(pinned, "presence_penalty").Float(), kimiK3FixedSamplingEpsilon)
	require.InDelta(t, 0.5, gjson.GetBytes(pinned, "frequency_penalty").Float(), kimiK3FixedSamplingEpsilon)
	_, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", pinned)
	var invalid *OpenAIInvalidParameterError
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, "temperature", invalid.Param)

	two := applyNewAPIAliFixedSamplingShape("kimi-k3", []byte(`{"model":"kimi-k3","temperature":2}`))
	require.InDelta(t, 2, gjson.GetBytes(two, "temperature").Float(), kimiK3FixedSamplingEpsilon)
	_, err = applyMoonshotThinkingShape(nil, account, "kimi-k3", two)
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, "temperature", invalid.Param)

	legal := applyNewAPIAliFixedSamplingShape("kimi-k3", []byte(`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"temperature":0.6}`))
	require.InDelta(t, 1, gjson.GetBytes(legal, "temperature").Float(), kimiK3FixedSamplingEpsilon)
	require.InDelta(t, 0.95, gjson.GetBytes(legal, "top_p").Float(), kimiK3FixedSamplingEpsilon)
	got, err := applyMoonshotThinkingShape(nil, account, "kimi-k3", legal)
	require.NoError(t, err)
	require.InDelta(t, 1, gjson.GetBytes(got, "temperature").Float(), kimiK3FixedSamplingEpsilon)
	require.InDelta(t, 0.95, gjson.GetBytes(got, "top_p").Float(), kimiK3FixedSamplingEpsilon)
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
	for _, tc := range []struct{ body, param string }{
		{`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"n":2}`, "n"},
		{`{"model":"kimi-k3","messages":[{"role":"user","content":"hi"}],"temperature":1.1}`, "temperature"},
		{`{"model":"kimi-k3","messages":[{"role":"tool","content":"some tool result"},{"role":"user","content":"hello"}]}`, "messages"},
	} {
		t.Run(tc.param, func(t *testing.T) {
			body := []byte(tc.body)
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
			require.Equal(t, tc.param, gjson.Get(rec.Body.String(), "error.param").String())
			require.Equal(t, "invalid_parameter", gjson.Get(rec.Body.String(), "error.code").String())
		})
	}
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

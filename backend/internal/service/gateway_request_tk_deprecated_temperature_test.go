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

func TestTkStripDeprecatedTemperature_StripIsTopLevelOnly(t *testing.T) {
	input := []byte(`{"model":"claude-opus-4-7","temperature":0.7,"tools":[{"name":"set_temp","input_schema":{"type":"object","properties":{"temperature":{"type":"number"}}}}],"messages":[{"role":"user","content":"hi"}]}`)

	got := tkStripDeprecatedTemperature(input)

	require.False(t, gjson.GetBytes(got, "temperature").Exists())
	require.True(t, gjson.GetBytes(got, "tools.0.input_schema.properties.temperature").Exists())
	require.Equal(t, "claude-opus-4-7", gjson.GetBytes(got, "model").String())
	require.True(t, gjson.ValidBytes(got))
}

func TestTkStripDeprecatedTemperature_NoTouchForOlderClaudeModels(t *testing.T) {
	cases := []string{
		`{"model":"claude-opus-4-6","temperature":0.7,"messages":[]}`,
		`{"model":"claude-sonnet-4-6","temperature":0.7,"messages":[]}`,
		`{"model":"claude-haiku-4-5","temperature":0.7,"messages":[]}`,
		`{"model":"gpt-5.5","temperature":0.7,"messages":[]}`,
	}
	for _, body := range cases {
		t.Run(gjson.Get(body, "model").String(), func(t *testing.T) {
			got := tkStripDeprecatedTemperature([]byte(body))
			require.Equal(t, body, string(got))
		})
	}
}

func TestForwardAsChatCompletions_AnthropicOpus47StripsTemperature(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-opus-4-7","temperature":0.7,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
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
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_opus47_temperature"}},
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
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
	require.Equal(t, "claude-opus-4-7", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, http.StatusOK, rec.Code)
}

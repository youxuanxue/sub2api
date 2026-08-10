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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type bedrockCCUpstreamMock struct {
	lastReq *http.Request
}

func (u *bedrockCCUpstreamMock) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.doWithTLS(req)
}

func (u *bedrockCCUpstreamMock) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.doWithTLS(req)
}

func (u *bedrockCCUpstreamMock) doWithTLS(req *http.Request) (*http.Response, error) {
	u.lastReq = req
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	rec.Header().Set("x-amzn-requestid", "bedrock-req-1")
	rec.WriteHeader(http.StatusOK)
	_, _ = rec.WriteString(`{
		"id":"msg_bedrock_cc",
		"type":"message",
		"role":"assistant",
		"content":[{"type":"text","text":"BEDROCK-CC-OK"}],
		"model":"claude-sonnet-4-6",
		"stop_reason":"end_turn",
		"usage":{"input_tokens":7,"output_tokens":8}
	}`)
	return rec.Result(), nil
}

func TestForwardAsChatCompletions_BedrockAccountBridgesViaBedrockUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &bedrockCCUpstreamMock{}
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	svc := &GatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:       91,
		Name:     "bedrock-2",
		Platform: PlatformAnthropic,
		Type:     AccountTypeBedrock,
		Credentials: map[string]any{
			"auth_mode":        "apikey",
			"api_key":          "bedrock-test-key",
			"aws_region":       "us-east-1",
			"aws_force_global": "true",
		},
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "claude-sonnet-4-6", result.Model)
	require.False(t, result.Stream)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 8, result.Usage.OutputTokens)

	require.NotNil(t, upstream.lastReq)
	require.Contains(t, upstream.lastReq.URL.Host, "bedrock-runtime.")
	require.Contains(t, upstream.lastReq.URL.Path, "/model/")
	require.NotContains(t, upstream.lastReq.URL.Host, "anthropic.com")
	require.Equal(t, "Bearer bedrock-test-key", upstream.lastReq.Header.Get("Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("x-api-key"))

	reqBody, err := io.ReadAll(upstream.lastReq.Body)
	require.NoError(t, err)
	require.Equal(t, "bedrock-2023-05-31", gjson.GetBytes(reqBody, "anthropic_version").String())
	require.False(t, gjson.GetBytes(reqBody, "stream").Bool())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "chat.completion", gjson.GetBytes(rec.Body.Bytes(), "object").String())
	require.Equal(t, "BEDROCK-CC-OK", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
}

func TestForwardAsChatCompletions_BedrockAccountDoesNotUseAnthropicAPIKeyAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &bedrockCCUpstreamMock{}
	svc := &GatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:       91,
		Platform: PlatformAnthropic,
		Type:     AccountTypeBedrock,
		Credentials: map[string]any{
			"auth_mode":  "apikey",
			"api_key":    "bedrock-test-key",
			"aws_region": "us-east-1",
		},
	}

	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"max_tokens":16,"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
	require.NoError(t, err)
	require.NotContains(t, upstream.lastReq.URL.Host, "anthropic.com")
	require.NotContains(t, upstream.lastReq.URL.Path, "/v1/messages")
}

func TestHandleCCBufferedFromAnthropicJSON_BedrockBridgeRegression(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"x-request-id": []string{"bedrock-json"},
		},
		Body: io.NopCloser(strings.NewReader(`{
			"id":"bedrock-json",
			"type":"message",
			"role":"assistant",
			"content":[{"type":"text","text":"bedrock-json-ok"}],
			"model":"claude-sonnet-4-6",
			"stop_reason":"end_turn",
			"usage":{"input_tokens":3,"output_tokens":4}
		}`)),
	}

	svc := &GatewayService{}
	result, err := svc.handleCCBufferedFromAnthropicJSON(resp, c, "claude-sonnet-4-6", "claude-sonnet-4-6", nil, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "bedrock-json-ok", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
}

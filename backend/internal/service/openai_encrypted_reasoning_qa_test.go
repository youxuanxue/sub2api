package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestStashOpenAIEncryptedReasoningFromSSE_OutputItemDone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	stashOpenAIEncryptedReasoningFromSSE(c, []byte(`{"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":"gAAAA-ITEM"}}`))
	stashOpenAIEncryptedReasoningFromSSE(c, []byte(`{"type":"response.output_text.delta","delta":"hi"}`))
	stashOpenAIEncryptedReasoningFromSSE(c, []byte(`{"type":"response.completed","response":{"output":[{"id":"rs_1","type":"reasoning","encrypted_content":"gAAAA-ITEM"},{"id":"rs_2","type":"reasoning","encrypted_content":"gAAAA-DONE"}]}}`))

	got, ok := c.Get(openaiEncryptedReasoningGinKey)
	require.True(t, ok)
	blocks, ok := got.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 2)
	require.Contains(t, blocks[0], `"item_id":"rs_1"`)
	require.Contains(t, blocks[0], "gAAAA-ITEM")
	require.Contains(t, blocks[1], `"item_id":"rs_2"`)
	require.Contains(t, blocks[1], "gAAAA-DONE")
}

func TestOpenAIGatewayService_Forward_WSv2_StreamStashesEncryptedReasoning(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.output_item.done","item":{"id":"rs_ws_1","type":"reasoning","encrypted_content":"gAAAA-WS-STREAM","summary":[{"type":"summary_text","text":"plan"}]}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ws_enc_1","model":"gpt-5.1","output":[{"id":"rs_ws_1","type":"reasoning","summary":[{"type":"summary_text","text":"plan"}]}],"usage":{"input_tokens":2,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          41,
		Name:        "openai-ws-enc",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
	}

	body := []byte(`{"model":"gpt-5.1","stream":true,"input":[{"type":"input_text","text":"hi"}]}`)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)

	got, ok := c.Get(openaiEncryptedReasoningGinKey)
	require.True(t, ok, "WS stream 应把 reasoning.encrypted_content 写入 QA gin key")
	blocks, ok := got.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], `"item_id":"rs_ws_1"`)
	require.Contains(t, blocks[0], "gAAAA-WS-STREAM")
}

func TestOpenAIGatewayService_Forward_WSv2_NonStreamStashesEncryptedReasoningFromItemEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.output_item.done","item":{"id":"rs_ws_2","type":"reasoning","encrypted_content":"gAAAA-WS-JSON"}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ws_enc_2","model":"gpt-5.1","output":[{"id":"rs_ws_2","type":"reasoning","summary":[{"type":"summary_text","text":"plan"}]}],"usage":{"input_tokens":2,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          42,
		Name:        "openai-ws-enc-json",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
	}

	body := []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"input_text","text":"hi"}]}`)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)

	got, ok := c.Get(openaiEncryptedReasoningGinKey)
	require.True(t, ok, "非流 WS 也应从中间 output_item 事件 stash 密文，不能只看 completed.output")
	blocks, ok := got.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "gAAAA-WS-JSON")
}

func TestStashOpenAIEncryptedReasoningFromSSE_IgnoresEmptyAndNonReasoning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	stashOpenAIEncryptedReasoningFromSSE(c, []byte(`{"type":"response.output_item.done","item":{"id":"msg_1","type":"message","encrypted_content":""}}`))
	stashOpenAIEncryptedReasoningFromSSE(c, nil)
	_, ok := c.Get(openaiEncryptedReasoningGinKey)
	require.False(t, ok)
}

func TestHandleStreamingResponse_StashesEncryptedReasoning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				MaxLineSize: defaultMaxLineSize,
			},
		},
		toolCorrector: NewCodexToolCorrector(),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.output_item.done","item":{"id":"rs_http_1","type":"reasoning","encrypted_content":"gAAAA-HTTP-STREAM"}}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp_http_enc","output":[{"id":"rs_http_1","type":"reasoning","summary":[{"type":"summary_text","text":"plan"}]}],"usage":{"input_tokens":2,"output_tokens":1}}}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-http-enc"}},
	}

	result, err := svc.handleStreamingResponse(
		c.Request.Context(),
		resp,
		c,
		&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Name: "acc"},
		time.Now(),
		"gpt-5.6-sol",
		"gpt-5.6-sol",
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "gAAAA-HTTP-STREAM")

	got, ok := c.Get(openaiEncryptedReasoningGinKey)
	require.True(t, ok, "HTTP 非 passthrough 流式路径应把 item.encrypted_content 写入 QA gin key")
	blocks, ok := got.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], `"item_id":"rs_http_1"`)
	require.Contains(t, blocks[0], "gAAAA-HTTP-STREAM")
}

func TestHandleStreamingResponse_DoesNotStashWithoutCiphertext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				MaxLineSize: defaultMaxLineSize,
			},
		},
		toolCorrector: NewCodexToolCorrector(),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.output_item.done","item":{"id":"rs_http_2","type":"reasoning","summary":[{"type":"summary_text","text":"plan"}]}}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp_http_plain","usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
		}, "\n"))),
	}

	result, err := svc.handleStreamingResponse(
		c.Request.Context(),
		resp,
		c,
		&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Name: "acc"},
		time.Now(),
		"gpt-5.6-sol",
		"gpt-5.6-sol",
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	_, ok := c.Get(openaiEncryptedReasoningGinKey)
	require.False(t, ok)
}

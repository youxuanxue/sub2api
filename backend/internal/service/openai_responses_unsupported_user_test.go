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

// TestOpenAIGatewayService_Forward_StripsUserFieldFromAPIKeyResponsesPassthrough is a
// regression for upstream Wei-Shaw/sub2api#1264: clients such as LobeHub send a
// top-level `user` field on /v1/responses, but the OpenAI Responses API rejects
// it with `Unsupported parameter: user`. The OAuth path already strips it via
// applyCodexOAuthTransform; this test pins the parallel behavior on the APIKey
// passthrough path so that compat upstreams hosting /v1/responses do not blow
// up with HTTP 400.
func TestOpenAIGatewayService_Forward_StripsUserFieldFromAPIKeyResponsesPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","stream":false,"input":[{"role":"user","content":"hello"}],"user":"user_123","prompt_cache_retention":"24h","safety_identifier":"sid"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_strip_user"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_strip_user","status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	account := &Account{
		ID:          7777,
		Name:        "openai-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq, "upstream must be invoked")

	forwardedBody := upstream.lastBody
	require.False(t, gjson.GetBytes(forwardedBody, "user").Exists(),
		"user must be stripped before forwarding to /v1/responses upstream (upstream #1264)")
	require.False(t, gjson.GetBytes(forwardedBody, "prompt_cache_retention").Exists(),
		"prompt_cache_retention should remain stripped (existing behavior)")
	require.False(t, gjson.GetBytes(forwardedBody, "safety_identifier").Exists(),
		"safety_identifier should remain stripped (existing behavior)")
}

// TestCursorResponsesUnsupportedFields_IncludesUser pins the Cursor-style
// (isResponsesShape) raw forwarding path: when a Responses-shape body is POSTed
// to /v1/chat/completions, the same fields must be filtered before reaching the
// Responses upstream. This list is kept semantically in sync with the
// unsupportedFields list in openai_gateway_service.go; the symmetry is enforced
// by upstream #1264.
func TestCursorResponsesUnsupportedFields_IncludesUser(t *testing.T) {
	found := false
	for _, f := range cursorResponsesUnsupportedFields {
		if f == "user" {
			found = true
			break
		}
	}
	require.True(t, found, "cursorResponsesUnsupportedFields must include `user` (upstream #1264)")
}

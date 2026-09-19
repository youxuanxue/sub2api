package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAntigravityGatewayService_WrapNativeImageRequestUsesImageGenEnvelope(t *testing.T) {
	svc := &AntigravityGatewayService{}
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}]}`)

	wrappedBody, err := svc.wrapV1InternalRequest("project-image", "gemini-3.1-flash-image", body)
	require.NoError(t, err)
	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(wrappedBody, &wrapped))
	require.Equal(t, "image_gen", wrapped["requestType"])
	require.Regexp(t, `^image_gen/[0-9]+/.+/12$`, wrapped["requestId"])
	request, ok := wrapped["request"].(map[string]any)
	require.True(t, ok)
	gen, ok := request["generationConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []any{"TEXT", "IMAGE"}, gen["responseModalities"])
}

func TestAntigravityGatewayService_WrapNativeImageRequestPreservesExistingModalities(t *testing.T) {
	svc := &AntigravityGatewayService{}
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}],"generationConfig":{"responseModalities":["IMAGE"],"imageConfig":{"aspectRatio":"16:9"}}}`)

	wrappedBody, err := svc.wrapV1InternalRequest("project-image", "gemini-3.1-flash-image", body)
	require.NoError(t, err)
	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(wrappedBody, &wrapped))
	request, ok := wrapped["request"].(map[string]any)
	require.True(t, ok)
	gen, ok := request["generationConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []any{"IMAGE"}, gen["responseModalities"])
	img, ok := gen["imageConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "16:9", img["aspectRatio"])
}

func TestAntigravityGatewayService_ForwardGemini_NonStreamingCollectsStreamingUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.8-flash:generateContent", bytes.NewReader(body))

	upstream := &queuedHTTPUpstreamStub{
		responses: []*http.Response{{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1}}}\n\n",
			)),
		}},
		onCall: func(req *http.Request, _ *queuedHTTPUpstreamStub) {
			require.Contains(t, req.URL.String(), "/v1internal:streamGenerateContent")
			require.Contains(t, req.URL.String(), "alt=sse")
		},
	}
	svc := &AntigravityGatewayService{
		settingService: NewSettingService(&antigravitySettingRepoStub{}, &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}),
		tokenProvider:  &AntigravityTokenProvider{},
		httpUpstream:   upstream,
	}
	account := &Account{
		ID: 105, Name: "native-non-stream", Platform: PlatformAntigravity, Type: AccountTypeOAuth,
		Status: StatusActive, Concurrency: 1,
		Credentials: map[string]any{"access_token": "token", "project_id": "project-105", "model_mapping": map[string]any{"gemini-3.8-flash": "gemini-3.8-flash"}},
	}

	result, err := svc.ForwardGemini(context.Background(), c, account, "gemini-3.8-flash", "generateContent", false, body, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}`, rec.Body.String())
	require.Len(t, upstream.requestBodies, 1)
	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBodies[0], &wrapped))
	_, hasRequestType := wrapped["requestType"]
	require.False(t, hasRequestType, "plain text native generateContent must omit requestType")
	request, ok := wrapped["request"].(map[string]any)
	require.True(t, ok)
	require.Regexp(t, `^-[0-9]+$`, request["sessionId"])
}

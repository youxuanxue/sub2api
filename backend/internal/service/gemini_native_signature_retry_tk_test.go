package service

import (
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

func TestGeminiForwardNativeRepairsRejectedSignatureOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const invalid = `{"error":{"code":400,"message":"Invalid thought signature.","status":"INVALID_ARGUMENT"}}`
	const body = `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"id":"123"}},"thoughtSignature":"old-signature"}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"result":"OK"}}}]}]}`
	const success = `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK","thoughtSignature":"fresh-signature"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}`
	for _, tc := range []struct {
		name         string
		body         string
		first        string
		secondStatus int
		wantCalls    int
		wantStatus   int
	}{
		{"recover", body, invalid, 200, 2, 200},
		{"persistent_error_stops", body, invalid, 400, 2, 400},
		{"unrelated_400", body, `{"error":{"code":400,"message":"Invalid function arguments"}}`, 200, 1, 400},
		{"model_turn_error_is_not_signature_error", body, `{"error":{"code":400,"message":"Requests ending with a model turn are not supported."}}`, 200, 1, 400},
		{"no_signature", `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, invalid, 200, 1, 400},
		{"already_repaired", strings.ReplaceAll(body, "old-signature", geminiDummyThoughtSignature), invalid, 200, 1, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := geminiCompatResponse(400, "failed-id", tc.first)
			secondBody := invalid
			if tc.secondStatus == 200 {
				secondBody = "data: " + success + "\n\n"
			}
			second := geminiCompatResponse(tc.secondStatus, "final-id", secondBody)
			if tc.secondStatus == 200 {
				second.Header.Set("Content-Type", "text/event-stream")
			}
			upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{first, second}}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{ID: 47, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.8-flash:streamGenerateContent", strings.NewReader(tc.body))
			result, err := svc.ForwardNative(context.Background(), c, account, "gemini-3.8-flash", "streamGenerateContent", true, []byte(tc.body))
			require.Len(t, upstream.requests, tc.wantCalls)
			require.Equal(t, tc.wantStatus, recorder.Code)
			if tc.wantStatus == 200 {
				require.NoError(t, err)
				require.Equal(t, "final-id", result.RequestID)
				require.Equal(t, 3, result.Usage.InputTokens)
				require.Contains(t, recorder.Body.String(), "fresh-signature")
				require.NotContains(t, recorder.Body.String(), "Invalid thought signature")
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.JSONEq(t, tc.first, recorder.Body.String())
			}
			if tc.wantCalls == 2 {
				firstBody, err := io.ReadAll(upstream.requests[0].Body)
				require.NoError(t, err)
				require.Equal(t, "old-signature", gjson.GetBytes(firstBody, "contents.0.parts.0.thoughtSignature").String())
				retryBody, err := io.ReadAll(upstream.requests[1].Body)
				require.NoError(t, err)
				require.Equal(t, geminiDummyThoughtSignature, gjson.GetBytes(retryBody, "contents.0.parts.0.thoughtSignature").String())
				require.Equal(t, "123", gjson.GetBytes(retryBody, "contents.0.parts.0.functionCall.args.id").String())
				require.Equal(t, "OK", gjson.GetBytes(retryBody, "contents.1.parts.0.functionResponse.response.result").String())
				require.Equal(t, upstream.requests[0].URL.String(), upstream.requests[1].URL.String())
			}
		})
	}
}

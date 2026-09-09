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

const geminiToolImageBody = `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"id":9007199254740993}},"thoughtSignature":"keep-signature"}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"read_file","response":{"id":9007199254740993,"output":"Image read"},"parts":[{"inlineData":{"mimeType":"image/png","data":"existing"}}]}},{"inlineData":{"mimeType":"image/png","data":"new-image"}},{"text":"context"}]}]}`

func TestGeminiFunctionResponseImagesPreserveToolResult(t *testing.T) {
	body := []byte(geminiToolImageBody)
	got := tkNormalizeGeminiFunctionResponseImages(body)
	require.Equal(t, "user", gjson.GetBytes(got, "contents.1.role").String())
	require.Equal(t, int64(2), gjson.GetBytes(got, "contents.1.parts.#").Int())
	require.Equal(t, "existing", gjson.GetBytes(got, "contents.1.parts.0.functionResponse.parts.0.inlineData.data").String())
	require.Equal(t, "new-image", gjson.GetBytes(got, "contents.1.parts.0.functionResponse.parts.1.inlineData.data").String())
	require.Equal(t, "context", gjson.GetBytes(got, "contents.1.parts.1.text").String())
	for _, path := range []string{"contents.0", "contents.1.parts.0.functionResponse.response"} {
		require.Equal(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(got, path).Raw)
	}
	require.Equal(t, "call-1", gjson.GetBytes(got, "contents.1.parts.0.functionResponse.id").String())
	require.Equal(t, got, tkNormalizeGeminiFunctionResponseImages(got), "normalization must be idempotent")
}

func TestGeminiFunctionResponseImagesLeaveOtherTurnsUnchanged(t *testing.T) {
	for name, body := range map[string]string{
		"plain_image":    `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"img"}}]}]}`,
		"multiple_tools": `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"a","response":{}}},{"functionResponse":{"name":"b","response":{}}},{"inlineData":{"mimeType":"image/png","data":"img"}}]}]}`,
		"model_turn":     `{"contents":[{"role":"model","parts":[{"functionResponse":{"name":"a","response":{}}},{"inlineData":{"mimeType":"image/png","data":"img"}}]}]}`,
		"audio":          `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"a","response":{}}},{"inlineData":{"mimeType":"audio/wav","data":"audio"}}]}]}`,
		"invalid_json":   `{"contents":[`,
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, []byte(body), tkNormalizeGeminiFunctionResponseImages([]byte(body)))
		})
	}
}

func TestGeminiForwardNativeNormalizesVertexToolImages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, accountType, model, action string
		wantNormalized                   bool
	}{
		{"vertex_gemini3", AccountTypeServiceAccount, "gemini-3.8-flash", "generateContent", true},
		{"vertex_gemini2", AccountTypeServiceAccount, "gemini-2.5-flash", "generateContent", false},
		{"api_key", AccountTypeAPIKey, "gemini-3.8-flash", "generateContent", false},
		{"count_tokens", AccountTypeServiceAccount, "gemini-3.8-flash", "countTokens", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{geminiCompatResponse(200, "test-id", `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1},"totalTokens":3}`)}}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}, tokenProvider: NewGeminiTokenProvider(nil, &protocolTargetGeminiTokenCache{token: "test-token"}, nil)}
			account := &Account{ID: 74, Platform: PlatformGemini, Type: tc.accountType, Credentials: map[string]any{"api_key": "test-key", "service_account_json": `{"project_id":"test-project","private_key":"unused","client_email":"test@example.invalid"}`}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/"+tc.model+":"+tc.action, strings.NewReader(geminiToolImageBody))
			_, err := svc.ForwardNative(context.Background(), c, account, tc.model, tc.action, false, []byte(geminiToolImageBody))
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			wire, err := io.ReadAll(upstream.requests[0].Body)
			require.NoError(t, err)
			require.Equal(t, tc.wantNormalized, gjson.GetBytes(wire, "contents.1.parts.0.functionResponse.parts.1.inlineData").Exists())
			require.Equal(t, "keep-signature", gjson.GetBytes(wire, "contents.0.parts.0.thoughtSignature").String())
			require.Equal(t, "9007199254740993", gjson.GetBytes(wire, "contents.0.parts.0.functionCall.args.id").Raw)
		})
	}
}

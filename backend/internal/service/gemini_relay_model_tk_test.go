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
)

func TestResolveGeminiForwardModels_AntigravityRelayPreservesPublicModelForHop(t *testing.T) {
	t.Parallel()

	relay := &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://edge.example.com",
			"model_mapping": map[string]any{
				"gemini-3.6-flash": "gemini-3.6-flash-tiered",
			},
		},
	}
	mappedModel, requestModel := resolveGeminiForwardModels(relay, "gemini-3.6-flash")
	require.Equal(t, "gemini-3.6-flash-tiered", mappedModel)
	require.Equal(t, "gemini-3.6-flash", requestModel)
}

func TestResolveGeminiForwardModels_NonRelayUsesMappedModelForHop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		account *Account
	}{
		{
			name: "antigravity API key without relay base URL",
			account: &Account{
				Platform: PlatformAntigravity,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gemini-3.6-flash": "gemini-3.6-flash-tiered"},
				},
			},
		},
		{
			name: "Gemini API key with custom base URL",
			account: &Account{
				Platform: PlatformGemini,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"base_url":      "https://gemini-proxy.example.com",
					"model_mapping": map[string]any{"gemini-3.6-flash": "gemini-3.6-flash-tiered"},
				},
			},
		},
		{
			name: "Vertex service account",
			account: &Account{
				Platform: PlatformGemini,
				Type:     AccountTypeServiceAccount,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gemini-3.6-flash": "gemini-3.6-flash-tiered"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mappedModel, requestModel := resolveGeminiForwardModels(tt.account, "gemini-3.6-flash")
			require.Equal(t, "gemini-3.6-flash-tiered", mappedModel)
			require.Equal(t, mappedModel, requestModel)
		})
	}
}

func TestGeminiCompat_AntigravityRelayUsesPublicModelAcrossIngresses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		publicModel = "gemini-3.6-flash"
		wireModel   = "gemini-3.6-flash-tiered"
		wantURL     = "https://edge.example.com/antigravity/v1beta/models/gemini-3.6-flash:generateContent"
	)

	tests := []struct {
		name       string
		requestURL string
		body       []byte
		forward    func(*GeminiMessagesCompatService, *gin.Context, *Account, []byte) (*ForwardResult, error)
	}{
		{
			name:       "messages",
			requestURL: "/v1/messages",
			body:       []byte(`{"model":"gemini-3.6-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`),
			forward: func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return svc.Forward(context.Background(), c, account, body)
			},
		},
		{
			name:       "native",
			requestURL: "/antigravity/v1beta/models/gemini-3.6-flash:generateContent",
			body:       []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
			forward: func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return svc.ForwardNative(context.Background(), c, account, publicModel, "generateContent", false, body)
			},
		},
		{
			name:       "chat completions",
			requestURL: "/v1/chat/completions",
			body:       []byte(`{"model":"gemini-3.6-flash","messages":[{"role":"user","content":"hi"}]}`),
			forward: func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return svc.ForwardAsChatCompletions(context.Background(), c, account, body)
			},
		},
		{
			name:       "responses",
			requestURL: "/v1/responses",
			body:       []byte(`{"model":"gemini-3.6-flash","input":"hi"}`),
			forward: func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
				return svc.ForwardAsResponses(context.Background(), c, account, body)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpStub := &geminiCompatHTTPUpstreamStub{
				response: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(
						`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
					)),
				},
			}
			svc := &GeminiMessagesCompatService{httpUpstream: httpStub, cfg: &config.Config{}}
			account := &Account{
				ID:          61,
				Platform:    PlatformAntigravity,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
				Credentials: map[string]any{
					"api_key":  "relay-key",
					"base_url": "https://edge.example.com",
					"model_mapping": map[string]any{
						publicModel: wireModel,
					},
				},
			}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, tt.requestURL, bytes.NewReader(tt.body))

			result, err := tt.forward(svc, c, account, tt.body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, publicModel, result.Model)
			require.Equal(t, wireModel, result.UpstreamModel)
			require.NotNil(t, httpStub.lastReq)
			require.Equal(t, wantURL, httpStub.lastReq.URL.String())
			require.NotContains(t, httpStub.lastReq.URL.String(), wireModel)
		})
	}
}

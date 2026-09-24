//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type geminiAccountTestUpstream struct {
	request  *http.Request
	response *http.Response
}

func (u *geminiAccountTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.request = req
	return u.response, nil
}

func (u *geminiAccountTestUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestCreateGeminiTestPayload_ImageModel(t *testing.T) {
	t.Parallel()

	payload := createGeminiTestPayload(nil, "gemini-2.5-flash-image", "draw a tiny robot")

	var parsed struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			ResponseModalities []string `json:"responseModalities"`
			ImageConfig        struct {
				AspectRatio string `json:"aspectRatio"`
			} `json:"imageConfig"`
		} `json:"generationConfig"`
	}

	require.NoError(t, json.Unmarshal(payload, &parsed))
	require.Len(t, parsed.Contents, 1)
	require.Len(t, parsed.Contents[0].Parts, 1)
	require.Equal(t, "draw a tiny robot", parsed.Contents[0].Parts[0].Text)
	require.Equal(t, []string{"TEXT", "IMAGE"}, parsed.GenerationConfig.ResponseModalities)
	require.Equal(t, "1:1", parsed.GenerationConfig.ImageConfig.AspectRatio)
}

func TestProcessGeminiStream_EmitsImageEvent(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	ctx, recorder := newTestContext()
	svc := &AccountTestService{}

	stream := strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"},{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"QUJD\"}}]}}]}\n\ndata: [DONE]\n\n")

	err := svc.processGeminiStream(ctx, stream)
	require.NoError(t, err)

	body := recorder.Body.String()
	require.Contains(t, body, "\"type\":\"content\"")
	require.Contains(t, body, "\"text\":\"ok\"")
	require.Contains(t, body, "\"type\":\"image\"")
	require.Contains(t, body, "\"image_url\":\"data:image/png;base64,QUJD\"")
	require.Contains(t, body, "\"mime_type\":\"image/png\"")
}

func TestGeminiAccountConnection_UsesPublicModelOnlyForAntigravityRelayHop(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		publicModel = "gemini-3.6-flash"
		wireModel   = "gemini-3.6-flash-tiered"
	)
	tests := []struct {
		name     string
		platform string
		wantPath string
	}{
		{
			name:     "Antigravity Edge relay",
			platform: PlatformAntigravity,
			wantPath: "/antigravity/v1beta/models/" + publicModel + ":streamGenerateContent",
		},
		{
			name:     "direct Gemini API key",
			platform: PlatformGemini,
			wantPath: "/v1beta/models/" + wireModel + ":streamGenerateContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &geminiAccountTestUpstream{
				response: &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(
						"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n",
					)),
				},
			}
			svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{
				ID:          61,
				Platform:    tt.platform,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
				Credentials: map[string]any{
					"api_key":  "test-key",
					"base_url": "https://edge.example.com",
					"model_mapping": map[string]any{
						publicModel: wireModel,
					},
				},
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/61/test", nil)

			require.NoError(t, svc.testGeminiAccountConnection(ctx, account, publicModel, "hi"))
			require.NotNil(t, upstream.request)
			require.Equal(t, tt.wantPath, upstream.request.URL.Path)
			require.Equal(t, "sse", upstream.request.URL.Query().Get("alt"))
			require.NotContains(t, recorder.Body.String(), "\"model\":\""+wireModel+"\"")
			require.Contains(t, recorder.Body.String(), "\"model\":\""+publicModel+"\"")
			require.Contains(t, recorder.Body.String(), "\"text\":\"ok\"")
		})
	}
}

func TestGeminiWebAdminTestUsesWorkerReferenceAndSupportedPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"worker", "relay", "ordinary"} {
		for _, image := range []bool{false, true} {
			t.Run(kind+"/image="+strconv.FormatBool(image), func(t *testing.T) {
				model, mapped := "gemini-3.8-flash", "gemini-web-flash"
				if image {
					model, mapped = "gemini-3.1-flash-image", "gemini-web-pro-image"
				}
				account := &Account{ID: 38, Platform: PlatformGemini, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"api_key": "worker-key", "base_url": "https://worker.example.test",
						"model_mapping": map[string]any{model: mapped}}}
				wantReference := ""
				switch kind {
				case "worker":
					account.Credentials["gemini_web"] = map[string]any{"runtime": map[string]any{"version": 1}}
					wantReference = "38"
				case "relay":
					account.Credentials[GeminiWebRelayCredentialKey] = true
				}
				upstream := &geminiAccountTestUpstream{response: &http.Response{StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n"))}}
				svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
				ctx, recorder := newTestContext()
				ctx.Request.Header.Set("X-TokenKey-Gemini-Web-Account-ID", "999")
				require.NoError(t, svc.testGeminiAccountConnection(ctx, account, model, "hi"))
				require.Equal(t, wantReference, upstream.request.Header.Get("X-TokenKey-Gemini-Web-Account-ID"))
				require.Equal(t, "worker-key", upstream.request.Header.Get("x-goog-api-key"))
				require.Equal(t, "/v1beta/models/"+mapped+":streamGenerateContent", upstream.request.URL.Path)
				body, err := io.ReadAll(upstream.request.Body)
				require.NoError(t, err)
				var payload map[string]any
				require.NoError(t, json.Unmarshal(body, &payload))
				if kind != "ordinary" {
					require.True(t, geminiWebNativeBodySupported(body, image), string(body))
					require.NotContains(t, payload, "systemInstruction")
					if image {
						require.Equal(t, map[string]any{"responseModalities": []any{"TEXT", "IMAGE"}}, payload["generationConfig"])
					}
				} else if image {
					require.Equal(t, map[string]any{"aspectRatio": "1:1"}, payload["generationConfig"].(map[string]any)["imageConfig"])
				} else {
					require.Equal(t, map[string]any{"parts": []any{map[string]any{"text": "You are a helpful AI assistant."}}}, payload["systemInstruction"])
				}
				require.Equal(t, []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}}, payload["contents"])
				require.Contains(t, recorder.Body.String(), `"success":true`)
			})
		}
	}
}

//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

	payload := createGeminiTestPayload("gemini-2.5-flash-image", "draw a tiny robot")

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
		name         string
		platform     string
		wantURLModel string
	}{
		{name: "Antigravity Edge relay", platform: PlatformAntigravity, wantURLModel: publicModel},
		{name: "direct Gemini API key", platform: PlatformGemini, wantURLModel: wireModel},
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
			require.Contains(t, upstream.request.URL.Path, "/models/"+tt.wantURLModel+":streamGenerateContent")
			require.NotContains(t, recorder.Body.String(), "\"model\":\""+wireModel+"\"")
			require.Contains(t, recorder.Body.String(), "\"model\":\""+publicModel+"\"")
			require.Contains(t, recorder.Body.String(), "\"text\":\"ok\"")
		})
	}
}

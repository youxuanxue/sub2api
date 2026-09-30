//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type geminiImagesIngressRepo struct{ candidateNativeRepo }

func (r *geminiImagesIngressRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	var result []service.Account
	for _, account := range r.accounts {
		if account.Platform == platform {
			result = append(result, account)
		}
	}
	return result, nil
}

type geminiImagesIngressUpstream struct {
	service.HTTPUpstream
	body     string
	stream   bool
	requests [][]byte
}

func (u *geminiImagesIngressUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.requests = append(u.requests, body)
	mime := "application/json"
	if u.stream {
		mime = "text/event-stream"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {mime}}, Body: io.NopCloser(strings.NewReader(u.body))}, nil
}
func (u *geminiImagesIngressUpstream) DoWithTLS(req *http.Request, proxy string, account int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, account, concurrency)
}

func TestUS059_GeminiImagesIngressGenerationAndSettlement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	encoded := base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	for _, transport := range []string{"oauth", "relay"} {
		for _, scenario := range []string{"default", "explicit", "empty", "partial", "invalid", "multiple"} {
			t.Run(transport+"/"+scenario, func(t *testing.T) {
				data := encoded
				if scenario == "invalid" {
					data = "not base64!"
				}
				parts := `{"inlineData":{"mimeType":"image/png","data":"` + data + `"}}`
				if scenario == "empty" {
					parts = `{"text":"Cannot generate"}`
				}
				if scenario == "multiple" {
					parts += "," + parts
				}
				finish := `,"finishReason":"STOP"`
				if scenario == "partial" {
					finish = ""
				}
				upstreamBody := `{"candidates":[{"content":{"role":"model","parts":[` + parts + `]}` + finish + `}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":5}}`
				upstream := &geminiImagesIngressUpstream{body: upstreamBody}
				if transport == "oauth" {
					upstream.stream = true
					upstream.body = "data: {\"response\":" + upstreamBody + "}\n\n"
				}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				group := &service.Group{ID: 740, Platform: service.PlatformNewAPI, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1, AllowImageGeneration: true}
				account := service.Account{ID: 200, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{740}, Credentials: map[string]any{"access_token": "test-only", "project_id": "test-project", "plan_type": "Pro", "model_mapping": map[string]any{"nano-2": "gemini-3.1-flash-image"}}}
				if transport == "relay" {
					account.Type = service.AccountTypeAPIKey
					account.Credentials["api_key"] = "test-only"
					account.Credentials["base_url"] = "https://api-us4.tokenkey.dev"
				}
				attachHandlerTestProtocolCapability(t, &account, protocolrouter.ProtocolGeminiGenerateContent)
				peer := account
				peer.ID = 201
				peer.Priority = 1
				repo := &geminiImagesIngressRepo{candidateNativeRepo: candidateNativeRepo{accounts: []service.Account{account, peer}}}
				usage := &responsesIngressUsageRepo{}
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				defer billing.Stop()
				gateway := service.NewGatewayService(repo, &fakeGroupRepo{group: group}, usage, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billing, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				gemini := service.NewGeminiMessagesCompatService(repo, nil, nil, nil, nil, nil, upstream, nil, cfg)
				ag := service.NewAntigravityGatewayService(repo, nil, nil, &service.AntigravityTokenProvider{}, nil, upstream, service.NewSettingService(nil, cfg), nil, nil, nil)
				h := NewGatewayHandler(gateway, nil, gemini, ag, nil, service.NewConcurrencyService(nil), billing, nil, nil, nil, nil, nil, nil, cfg, nil)
				h.SetProtocolRouter(service.NewProtocolRouter())
				options := ""
				wantSize := "2K"
				wantRatio := "1:1"
				if scenario == "explicit" {
					options = `,"size":"4K","aspect_ratio":"3:4"`
					wantSize = "4K"
					wantRatio = "3:4"
				}
				body := fmt.Sprintf(`{"model":"nano-2","prompt":"Draw a cup"%s}`, options)
				path := "/v1/images/generations"
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(context.Background())
				key := &service.APIKey{ID: 638, UserID: 1, GroupID: &group.ID, Group: group, User: &service.User{ID: 1, Balance: 10}}
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
				keys := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
				service.ProvideTKUniversalModelsProvider(keys, gateway, nil, &service.OpenAIGatewayService{}, h.protocolRouter)
				_, err := keys.UniversalResolver().PrepareCandidateIngress(c, key, service.ShapeOpenAIImages, path, "nano-2", []byte(body), "")
				require.NoError(t, err)
				h.GeminiImageGenerations(c)
				require.Len(t, upstream.requests, 1, "generation must never replay after output")
				prefix := ""
				if transport == "oauth" {
					prefix = "request."
				}
				require.Equal(t, wantSize, gjson.GetBytes(upstream.requests[0], prefix+"generationConfig.imageConfig.imageSize").String())
				require.Equal(t, wantRatio, gjson.GetBytes(upstream.requests[0], prefix+"generationConfig.imageConfig.aspectRatio").String())
				if scenario != "default" && scenario != "explicit" {
					require.Equal(t, 502, rec.Code, rec.Body.String())
					require.Empty(t, usage.logs, "undeliverable buffered images must not settle")
					return
				}
				require.Equal(t, 200, rec.Code, rec.Body.String())
				result, err := base64.StdEncoding.DecodeString(gjson.Get(rec.Body.String(), "data.0.b64_json").String())
				require.NoError(t, err)
				decoded, err := png.Decode(bytes.NewReader(result))
				require.NoError(t, err)
				require.Equal(t, image.Rect(0, 0, 2, 3), decoded.Bounds())
				require.Len(t, usage.logs, 1)
				log := usage.logs[0]
				require.Equal(t, account.ID, log.AccountID)
				require.Equal(t, 1, log.ImageCount)
				require.NotNil(t, log.ImageSize)
				require.Equal(t, wantSize, *log.ImageSize)
				require.NotNil(t, log.InboundEndpoint)
				require.Equal(t, path, *log.InboundEndpoint)
			})
		}
	}
}

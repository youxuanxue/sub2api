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
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type geminiPartialUsageUpstream struct {
	service.HTTPUpstream
	body   string
	stream bool
	calls  int
}

func (u *geminiPartialUsageUpstream) Do(request *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	mime := "application/json"
	if u.stream {
		mime = "text/event-stream"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {mime}}, Body: io.NopCloser(strings.NewReader(u.body))}, nil
}

func TestUS057_GeminiCompatIngressMetersDeliveredPartialImageOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var imageBytes bytes.Buffer
	require.NoError(t, png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	imageData := base64.StdEncoding.EncodeToString(imageBytes.Bytes())
	for _, endpoint := range []string{"chat", "responses"} {
		for _, scenario := range []string{"complete", "partial", "malformed_buffered", "malformed_stream"} {
			t.Run(endpoint+"/"+scenario, func(t *testing.T) {
				stream := scenario != "malformed_buffered"
				finish := ` ,"finishReason":"STOP"`
				if scenario == "partial" {
					finish = ""
				}
				parts := `{"inlineData":{"mimeType":"image/png","data":"` + imageData + `"}}`
				if strings.HasPrefix(scenario, "malformed") {
					parts += `,{"inlineData":{"mimeType":"image/png","data":"invalid!"}}`
				}
				upstreamBody := `{"candidates":[{"content":{"parts":[` + parts + `]} ` + finish + `}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":5}}`
				if stream {
					upstreamBody = "data: " + upstreamBody + "\n\n"
				}
				upstream := &geminiPartialUsageUpstream{body: upstreamBody, stream: stream}
				const model = "nano-2"
				cfg := &config.Config{RunMode: config.RunModeSimple}
				group := &service.Group{ID: 740, Platform: service.PlatformGemini, Status: service.StatusActive, Hydrated: true, RateMultiplier: 1, AllowImageGeneration: true}
				account := service.Account{ID: 200, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{group.ID}, Credentials: map[string]any{"api_key": "test-only", "base_url": "https://example.invalid", "model_mapping": map[string]any{model: "gemini-3.1-flash-image"}, service.GeminiWebRelayCredentialKey: true}}
				attachHandlerTestProtocolCapability(t, &account, protocolrouter.ProtocolGeminiGenerateContent)
				account.ProtocolEndpointCapability.ProbeEvidence = service.ProtocolProbeEvidence{NativeDeclaration: true}
				// A second eligible account makes accidental replay observable.
				peer := account
				peer.ID, peer.Priority = 201, 1
				repo := &candidateNativeRepo{accounts: []service.Account{account, peer}}
				usage := &responsesIngressUsageRepo{}
				billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				defer billingCache.Stop()
				gateway := service.NewGatewayService(repo, &fakeGroupRepo{group: group}, usage, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billingCache, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				gemini := service.NewGeminiMessagesCompatService(repo, nil, nil, nil, nil, nil, upstream, nil, cfg)
				h := NewGatewayHandler(gateway, nil, gemini, nil, nil, service.NewConcurrencyService(nil), billingCache, nil, nil, nil, nil, nil, nil, cfg, nil)
				h.SetProtocolRouter(service.NewProtocolRouter())
				body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Draw an apple"}],"stream":%t}`, model, stream)
				path := "/v1/chat/completions"
				if endpoint == "responses" {
					path = "/v1/responses"
					body = fmt.Sprintf(`{"model":%q,"input":"Draw an apple","stream":%t}`, model, stream)
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(context.Background())
				key := &service.APIKey{ID: 638, UserID: 1, GroupID: &group.ID, Group: group, User: &service.User{ID: 1, Balance: 10}}
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
				keys := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
				service.ProvideTKUniversalModelsProvider(keys, gateway, nil, &service.OpenAIGatewayService{}, h.protocolRouter)
				_, err := keys.UniversalResolver().PrepareCandidateIngress(c, key, service.ShapeOpenAIChat, path, model, []byte(body), "")
				require.NoError(t, err)
				if endpoint == "chat" {
					h.ChatCompletions(c)
				} else {
					h.Responses(c)
				}
				require.Equal(t, 1, upstream.calls, "a terminal partial stream must never replay")
				if strings.HasPrefix(scenario, "malformed") {
					require.NotContains(t, rec.Body.String(), imageData)
					if scenario == "malformed_buffered" {
						require.Empty(t, usage.logs, "undelivered buffered output has no result to settle")
					} else {
						require.Len(t, usage.logs, 1, "a failed opened stream retains its zero-usage audit record")
						require.Zero(t, usage.logs[0].ImageCount)
						require.Zero(t, usage.logs[0].InputTokens)
						require.Zero(t, usage.logs[0].OutputTokens)
						require.Zero(t, usage.logs[0].TotalCost)
						require.Zero(t, usage.logs[0].ActualCost)
					}
					return
				}
				require.Contains(t, rec.Body.String(), "data:image/png;base64,"+imageData)
				require.Len(t, usage.logs, 1, "both successful and delivered partial output must settle exactly once")
				require.Equal(t, account.ID, usage.logs[0].AccountID)
				require.Equal(t, 7, usage.logs[0].InputTokens)
				require.Equal(t, 5, usage.logs[0].OutputTokens)
				require.Equal(t, 1, usage.logs[0].ImageCount)
				if scenario == "partial" {
					require.NotContains(t, rec.Body.String(), "data: [DONE]")
					require.NotContains(t, rec.Body.String(), "response.completed")
				}
			})
		}
	}
}

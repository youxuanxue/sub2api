//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// This exercises the production Plan, pre-send account reload, executor profile,
// request/response converters, and HTTP transport boundary together. The only
// replaced dependency is the upstream HTTP response; no live generation occurs.
func TestUS057_GeminiNativeProtocolTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	imageData := base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	const publicModel = "nano-2"
	const wireModel = "gemini-3.1-flash-image"
	const prompt = "Draw a red apple"
	const generation = `"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"aspectRatio":"4:3"}}`
	cases := []struct {
		name     string
		protocol protocolrouter.Protocol
		path     string
		body     string
		textPath string
		forward  func(context.Context, *GeminiMessagesCompatService, *gin.Context, *Account, []byte) (*ForwardResult, error)
	}{
		{"messages", protocolrouter.ProtocolMessages, "/v1/messages", `{"model":"nano-2","max_tokens":128,"messages":[{"role":"user","content":"Draw a red apple"}],` + generation + `}`, "content.#.text", func(ctx context.Context, s *GeminiMessagesCompatService, c *gin.Context, a *Account, b []byte) (*ForwardResult, error) {
			return s.Forward(ctx, c, a, b)
		}},
		{"chat", protocolrouter.ProtocolChatCompletions, "/v1/chat/completions", `{"model":"nano-2","messages":[{"role":"user","content":"Draw a red apple"}],` + generation + `}`, "choices.0.message.content", func(ctx context.Context, s *GeminiMessagesCompatService, c *gin.Context, a *Account, b []byte) (*ForwardResult, error) {
			return s.ForwardAsChatCompletions(ctx, c, a, b)
		}},
		{"responses", protocolrouter.ProtocolResponses, "/v1/responses", `{"model":"nano-2","input":"Draw a red apple",` + generation + `}`, "output.0.content.#.text", func(ctx context.Context, s *GeminiMessagesCompatService, c *gin.Context, a *Account, b []byte) (*ForwardResult, error) {
			return s.ForwardAsResponses(ctx, c, a, b)
		}},
		{"native", protocolrouter.ProtocolGeminiGenerateContent, "/v1beta/models/nano-2:generateContent", `{"contents":[{"role":"user","parts":[{"text":"Draw a red apple"}]}],` + generation + `}`, "", func(ctx context.Context, s *GeminiMessagesCompatService, c *gin.Context, a *Account, b []byte) (*ForwardResult, error) {
			return s.ForwardNative(ctx, c, a, publicModel, "generateContent", false, b)
		}},
	}
	// Cover explicitly supplied client budgets as well as omitted defaults.
	for _, original := range cases {
		limitField := ""
		switch original.protocol {
		case protocolrouter.ProtocolChatCompletions:
			limitField = "max_completion_tokens"
		case protocolrouter.ProtocolResponses:
			limitField = "max_output_tokens"
		default:
			continue
		}
		limited := original
		limited.name += "_explicit_limit"
		var root map[string]any
		require.NoError(t, json.Unmarshal([]byte(original.body), &root))
		root[limitField] = 128
		body, err := json.Marshal(root)
		require.NoError(t, err)
		limited.body = string(body)
		cases = append(cases, limited)
	}
	for _, web := range []bool{false, true} {
		profileName := "generic"
		if web {
			profileName = "web"
		}
		for _, tc := range cases {
			t.Run(profileName+"/"+tc.name, func(t *testing.T) {
				body := []byte(tc.body)
				responsesPath := protocolrouter.ResponsesPathNone
				if tc.protocol == protocolrouter.ProtocolResponses {
					responsesPath = protocolrouter.ResponsesPathRoot
				}
				request, err := protocolrouter.ParseCanonicalRequest(tc.protocol, responsesPath, publicModel, false, body)
				require.NoError(t, err)
				account := &Account{ID: 200, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "test-only-key", "base_url": "https://gemini.example.com", "model_mapping": map[string]any{publicModel: wireModel}}}
				if web {
					account.Credentials[GeminiWebRelayCredentialKey] = true
				}
				attachTestNativeDeclaredCapability(account)
				snapshot, err := protocolAccountSnapshotForRequest(account, request)
				require.NoError(t, err)
				router := NewProtocolRouter()
				plan, err := router.Plan(request, snapshot)
				require.NoError(t, err)
				hasLimit := tc.protocol == protocolrouter.ProtocolMessages || strings.HasSuffix(tc.name, "_explicit_limit")
				if web && hasLimit {
					require.Equal(t, "gemini_web_best_effort_token_limit", plan.Adjustment())
					require.Equal(t, request.Digest(), plan.RequestDigest())
					require.NotEqual(t, request.Digest(), plan.EffectiveRequestDigest())
				} else {
					require.Equal(t, "", plan.Adjustment())
				}
				require.Equal(t, protocolrouter.GeminiEndpointNativeAPIKey, plan.GeminiProfile())
				require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, plan.TargetProtocol())
				upstreamJSON := `{"candidates":[{"content":{"role":"model","parts":[{"text":"apple"},{"inlineData":{"mimeType":"image/png","data":"` + imageData + `"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4}}`
				stub := &geminiCompatHTTPUpstreamStub{response: geminiCompatResponse(http.StatusOK, "native-test-request", upstreamJSON)}
				svc := &GeminiMessagesCompatService{httpUpstream: stub, cfg: &config.Config{}}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				ctx := WithProtocolRouting(context.Background(), router, request)
				c.Request = httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body)).WithContext(ctx)
				value, err := ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account,
					func(_ context.Context, _ *Account, endpoint string) error {
						require.Equal(t, plan.Endpoint(), endpoint)
						return nil
					},
					func(context.Context, int64) (*Account, error) { return account, nil },
					protocolExecutorsForTest(plan, func(executionCtx context.Context, executionAccount *Account, p protocolrouter.Plan, r protocolrouter.CanonicalRequest) (any, error) {
						return ExecuteGeminiProtocolProfile(p.GeminiProfile(), func() (*ForwardResult, error) {
							t.Fatal("native Gemini must not use cloudcode transport")
							return nil, nil
						}, func() (*ForwardResult, error) { return tc.forward(executionCtx, svc, c, executionAccount, r.Body()) })
					}))
				require.NoError(t, err)
				result := value.(*ForwardResult)
				require.Equal(t, 1, stub.calls)
				require.Equal(t, http.MethodPost, stub.lastReq.Method)
				require.Equal(t, "https://gemini.example.com/v1beta/models/"+wireModel+":generateContent", stub.lastReq.URL.String())
				require.Equal(t, "test-only-key", stub.lastReq.Header.Get("x-goog-api-key"))
				require.Empty(t, stub.lastReq.Header.Get("Authorization"))
				wireBody, err := io.ReadAll(stub.lastReq.Body)
				require.NoError(t, err)
				require.Equal(t, prompt, gjson.GetBytes(wireBody, "contents.0.parts.0.text").String())
				require.Equal(t, int64(1), gjson.GetBytes(wireBody, "contents.#").Int())
				require.False(t, gjson.GetBytes(wireBody, "systemInstruction").Exists())
				require.JSONEq(t, `["TEXT","IMAGE"]`, gjson.GetBytes(wireBody, "generationConfig.responseModalities").Raw)
				require.Equal(t, "4:3", gjson.GetBytes(wireBody, "generationConfig.imageConfig.aspectRatio").String())
				if web {
					require.False(t, gjson.GetBytes(wireBody, "generationConfig.maxOutputTokens").Exists())
				} else if hasLimit {
					require.Equal(t, int64(128), gjson.GetBytes(wireBody, "generationConfig.maxOutputTokens").Int())
				}
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Equal(t, 1, result.ImageCount)
				require.Equal(t, wireModel, result.UpstreamModel)
				if tc.protocol == protocolrouter.ProtocolGeminiGenerateContent {
					require.Equal(t, imageData, gjson.Get(recorder.Body.String(), "candidates.0.content.parts.1.inlineData.data").String())
				} else {
					var document any
					require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &document))
					// Require bytes in protocol-defined text fields, never an orphan extension.
					projection := gjson.Get(recorder.Body.String(), tc.textPath)
					text := projection.String()
					if projection.IsArray() {
						parts := make([]string, 0, len(projection.Array()))
						for _, part := range projection.Array() {
							parts = append(parts, part.String())
						}
						text = strings.Join(parts, "\n")
					}
					require.Contains(t, text, "data:image/png;base64,"+imageData)
					require.Contains(t, text, "apple")
				}
			})
		}
	}
}

func TestUS057_GeminiImageCandidateKeepsSelectedPlan(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		shape            UniversalShape
	}{
		{"messages", "/v1/messages", `{"model":"nano-2","max_tokens":128,"messages":[{"role":"user","content":"Draw an apple"}]}`, ShapeAnthropicMessages},
		{"native", "/v1beta/models/nano-2:generateContent", `{"contents":[{"parts":[{"text":"Draw an apple"}]}]}`, ShapeGemini},
		{"chat", "/v1/chat/completions", `{"model":"nano-2","messages":[{"role":"user","content":"Draw an apple"}]}`, ShapeOpenAIChat},
		{"responses", "/v1/responses", `{"model":"nano-2","input":"Draw an apple"}`, ShapeOpenAIChat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := webCandidateAccount(true)
			group := grp(740, PlatformGemini, 1, false)
			group.AllowImageGeneration = true
			group.AllowMessagesDispatch = true
			resolver, _, key := globalCandidateFixture([]Group{group}, []Account{account})
			ctx, state, err := resolver.PrepareCandidateRequest(context.Background(), key, tc.shape, tc.path, "nano-2", []byte(tc.body), "", "")
			require.NoError(t, err)
			selection, err := state.selectAccount(ctx, candidateSelectOptions{})
			require.NoError(t, err)
			require.Equal(t, account.ID, selection.Account.ID)
			require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, selection.ProtocolPlan.TargetProtocol())
			require.Equal(t, protocolrouter.GeminiEndpointNativeAPIKey, selection.ProtocolPlan.GeminiProfile())
			require.Equal(t, "https://example.invalid/v1beta/models/nano-2:generateContent", selection.ProtocolPlan.Endpoint())
		})
	}
}

//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiCompatImageOptionsPreserveNativeConfiguration(t *testing.T) {
	for _, imageConfig := range []string{`null`, `{"aspectRatio":"4:3"}`, `{"aspectRatio":"4:3","imageSize":"2K"}`} {
		t.Run(imageConfig, func(t *testing.T) {
			original := []byte(`{"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":` + imageConfig + `}}`)
			converted, err := preserveGeminiCompatOptions(original, []byte(`{"model":"gemini-test","max_tokens":8192,"messages":[{"role":"user","content":"draw an apple"}]}`))
			require.NoError(t, err)
			native, err := convertClaudeMessagesToGeminiGenerateContent(converted)
			require.NoError(t, err)
			require.JSONEq(t, gjson.GetBytes(original, "generationConfig").Raw, gjson.GetBytes(native, "generationConfig").Raw)
			require.Equal(t, "draw an apple", gjson.GetBytes(native, "contents.0.parts.0.text").String())
		})
	}
}

func TestGeminiCompatOptionsPreserveExplicitTokenLimits(t *testing.T) {
	for _, limit := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		converted, err := preserveGeminiCompatOptions([]byte(`{"`+limit+`":71}`), []byte(`{"max_tokens":71}`))
		require.NoError(t, err)
		require.Equal(t, int64(71), gjson.GetBytes(converted, "max_tokens").Int())
	}
}

func TestGeminiCompatImageOptionsRejectMalformedConfiguration(t *testing.T) {
	for _, config := range []string{`null`, `[]`, `{"responseModalities":null}`, `{"responseModalities":["AUDIO"]}`, `{"imageConfig":"4:3"}`, `{"unknown":true}`} {
		_, err := convertClaudeMessagesToGeminiGenerateContent([]byte(`{"messages":[{"role":"user","content":"draw"}],"generationConfig":` + config + `}`))
		require.Error(t, err, config)
	}
}

func TestGeminiCompatStreamsPreserveImagesOnce(t *testing.T) {
	for _, protocol := range []string{"messages", "chat", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			beginGeminiImageOutputObservation(c)
			first := geminiImageResponse(`{"text":"result:"},{"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}`)
			second := geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}},{"inline_data":{"mime_type":"image/jpeg","data":"aW1hZ2Uy"}}`)
			resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: " + first + "\n\ndata: " + second + "\n\ndata: {\"usageMetadata\":{\"promptTokenCount\":8}}\n\ndata: [DONE]\n\n"))}
			svc := &GeminiMessagesCompatService{}
			var err error
			switch protocol {
			case "messages":
				_, err = svc.handleStreamingResponse(c, resp, time.Now(), "gemini-test")
			case "chat":
				_, err = svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-test", false, false)
			case "responses":
				_, err = svc.handleResponsesStreamingResponseFromGemini(c, resp, time.Now(), "gemini-test", false)
			}
			require.NoError(t, err)
			var deltas strings.Builder
			for _, line := range strings.Split(recorder.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				event := strings.TrimPrefix(line, "data: ")
				switch protocol {
				case "messages":
					if gjson.Get(event, "type").String() == "content_block_delta" {
						deltas.WriteString(gjson.Get(event, "delta.text").String())
					}
				case "chat":
					deltas.WriteString(gjson.Get(event, "choices.0.delta.content").String())
				case "responses":
					if gjson.Get(event, "type").String() == "response.output_text.delta" {
						deltas.WriteString(gjson.Get(event, "delta").String())
					}
				}
			}
			require.Equal(t, "result:![image](data:image/png;base64,aW1hZ2Ux)![image](data:image/jpeg;base64,aW1hZ2Uy)", deltas.String())
			require.Equal(t, 2, observedGeminiImageOutputs(c))
		})
	}
}

func TestGeminiCompatNonStreamingMalformedImageFails(t *testing.T) {
	for _, protocol := range []string{"messages", "chat", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			beginGeminiImageOutputObservation(c)
			resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"invalid!"}}`)))}
			svc := &GeminiMessagesCompatService{}
			var err error
			switch protocol {
			case "messages":
				_, err = svc.handleNonStreamingResponse(c, resp, "gemini-test")
			case "chat":
				_, err = svc.handleChatCompletionsNonStreamingResponseFromGemini(c, resp, "gemini-test", false)
			case "responses":
				_, err = svc.handleResponsesNonStreamingResponseFromGemini(c, resp, "gemini-test", false)
			}
			require.Error(t, err)
			require.Equal(t, http.StatusBadGateway, recorder.Code)
			require.Equal(t, 0, observedGeminiImageOutputs(c))
		})
	}
}

func TestCollectGeminiSSERetainsEarlierImages(t *testing.T) {
	first := geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}`)
	second := geminiImageResponse(`{"text":"done"},{"inlineData":{"mimeType":"image/jpeg","data":"aW1hZ2Uy"}}`)
	collected, _, _, err := collectGeminiSSE(strings.NewReader("data: "+first+"\n\ndata: "+first+"\n\ndata: "+second+"\n\ndata: [DONE]\n\n"), false)
	require.NoError(t, err)
	raw, err := json.Marshal(collected)
	require.NoError(t, err)
	require.Equal(t, 2, len(geminiInlineImageOutputs(raw)))
	require.Equal(t, "done", gjson.GetBytes(raw, "candidates.0.content.parts.0.text").String())
}

func TestGeminiCompatStreamsRejectIncompleteOrMalformedEvents(t *testing.T) {
	incompleteImage := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}]}}],"usageMetadata":{"promptTokenCount":7}}`
	for _, protocol := range []string{"messages", "chat", "responses"} {
		for _, tt := range []struct {
			name, payload string
			images        int
			tokens        int
		}{
			{"malformed before content", `{"candidates":`, 0, 0},
			{"image without terminal", incompleteImage, 1, 7},
		} {
			t.Run(protocol+"/"+tt.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				beginGeminiImageOutputObservation(c)
				resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: " + tt.payload + "\n\n"))}
				svc := &GeminiMessagesCompatService{}
				var result *geminiStreamResult
				var err error
				switch protocol {
				case "messages":
					result, err = svc.handleStreamingResponse(c, resp, time.Now(), "gemini-test")
				case "chat":
					result, err = svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-test", false, false)
				case "responses":
					result, err = svc.handleResponsesStreamingResponseFromGemini(c, resp, time.Now(), "gemini-test", false)
				}
				require.Error(t, err)
				require.NotContains(t, recorder.Body.String(), `"message_stop"`)
				require.NotContains(t, recorder.Body.String(), `"response.completed"`)
				require.NotContains(t, recorder.Body.String(), `data: [DONE]`)
				require.Equal(t, tt.images, observedGeminiImageOutputs(c))
				require.Equal(t, tt.tokens, result.usage.InputTokens)
			})
		}
	}
}

func TestCollectGeminiSSERejectsMalformedImagesBeforeAggregation(t *testing.T) {
	for _, payload := range []string{`{"candidates":`, geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"invalid!"}}`)} {
		_, _, _, err := collectGeminiSSE(strings.NewReader("data: "+payload+"\n\ndata: "+geminiImageResponse(`{"text":"done"}`)+"\n\ndata: [DONE]\n\n"), false)
		require.Error(t, err)
	}
}

func TestCollectGeminiSSERejectsTruncatedImage(t *testing.T) {
	payload := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}]}}],"usageMetadata":{"promptTokenCount":7}}`
	_, usage, _, err := collectGeminiSSE(strings.NewReader("data: "+payload+"\n\n"), false)
	require.ErrorContains(t, err, "missing terminal event")
	require.Equal(t, 7, usage.InputTokens)
	// Google may end at EOF without [DONE] after an explicit finishReason.
	complete := geminiImageResponse(`{"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}`)
	collected, _, _, err := collectGeminiSSE(strings.NewReader("data: "+complete+"\n\n"), false)
	require.NoError(t, err)
	raw, err := json.Marshal(collected)
	require.NoError(t, err)
	require.Equal(t, 1, len(geminiInlineImageOutputs(raw)))
}

func TestGeminiCompatForwardPreservesInterruptedStreamUsage(t *testing.T) {
	imagePart := `{"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}`
	for _, protocol := range []string{"messages", "chat", "responses"} {
		for _, invalid := range []bool{false, true} {
			name := protocol + "/delivered-image"
			if invalid {
				name = protocol + "/undeliverable-mixed-frame"
			}
			t.Run(name, func(t *testing.T) {
				parts := imagePart
				if invalid {
					parts += `,{"inlineData":{"mimeType":"image/png","data":"invalid!"}}`
				}
				payload := `{"candidates":[{"content":{"parts":[` + parts + `]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":9}}`
				stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}}
				svc := &GeminiMessagesCompatService{httpUpstream: stub, cfg: &config.Config{}}
				account := &Account{ID: 101, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "test-key"}}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				body := []byte(`{"model":"gemini-2.5-flash","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"draw an apple"}]}`)
				if protocol == "responses" {
					body = []byte(`{"model":"gemini-2.5-flash","stream":true,"input":"draw an apple"}`)
				}
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, strings.NewReader(string(body)))
				var result *ForwardResult
				var err error
				switch protocol {
				case "messages":
					result, err = svc.Forward(context.Background(), c, account, body)
				case "chat":
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body)
				case "responses":
					result, err = svc.ForwardAsResponses(context.Background(), c, account, body)
				}
				require.Error(t, err)
				require.Equal(t, 1, stub.calls)
				require.NotNil(t, result)
				require.True(t, result.Stream)
				if invalid {
					require.Equal(t, 0, result.ImageCount)
					require.Zero(t, result.Usage.InputTokens)
					require.NotContains(t, rec.Body.String(), "![image]")
				} else {
					require.ErrorContains(t, err, "missing terminal event")
					require.Equal(t, 1, result.ImageCount)
					require.Equal(t, 7, result.Usage.InputTokens)
					require.Equal(t, 9, result.Usage.OutputTokens)
					require.Contains(t, rec.Body.String(), "![image](data:image/png;base64,aW1hZ2Ux)")
				}
				require.NotContains(t, rec.Body.String(), `"message_stop"`)
				require.NotContains(t, rec.Body.String(), `"response.completed"`)
				require.NotContains(t, rec.Body.String(), "data: [DONE]")
			})
		}
	}
}

func TestGeminiCompatForwardBufferedTruncationDoesNotBillUnreturnedImage(t *testing.T) {
	payload := `{"response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1hZ2Ux"}}]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":9}}}`
	for _, protocol := range []string{"messages", "chat", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}}
			svc := &GeminiMessagesCompatService{tokenProvider: &GeminiTokenProvider{}, httpUpstream: stub, cfg: &config.Config{}}
			account := &Account{ID: 101, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "test-token", "project_id": "project-test"}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := []byte(`{"model":"gemini-2.5-flash","max_tokens":128,"messages":[{"role":"user","content":"draw an apple"}]}`)
			if protocol == "responses" {
				body = []byte(`{"model":"gemini-2.5-flash","input":"draw an apple"}`)
			}
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, strings.NewReader(string(body)))
			var result *ForwardResult
			var err error
			switch protocol {
			case "messages":
				result, err = svc.Forward(context.Background(), c, account, body)
			case "chat":
				result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body)
			case "responses":
				result, err = svc.ForwardAsResponses(context.Background(), c, account, body)
			}
			require.Error(t, err)
			require.Equal(t, 1, stub.calls)
			require.Nil(t, result)
			require.Equal(t, http.StatusBadGateway, rec.Code)
			require.NotContains(t, rec.Body.String(), "![image]")
			require.Zero(t, observedGeminiImageOutputs(c))
		})
	}
}

func TestGeminiCompatForwardPolicyBlockIsTerminal(t *testing.T) {
	for _, protocol := range []string{"messages", "chat", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, blocked := range []bool{false, true} {
				name := protocol + "/buffered/unspecified"
				if stream {
					name = protocol + "/stream/unspecified"
				}
				if blocked {
					name = strings.Replace(name, "unspecified", "safety", 1)
				}
				t.Run(name, func(t *testing.T) {
					reason := "BLOCKED_REASON_UNSPECIFIED"
					if blocked {
						reason = "SAFETY"
					}
					payload := `{"response":{"promptFeedback":{"blockReason":"` + reason + `"},"usageMetadata":{"promptTokenCount":10}}}`
					stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}}
					svc := &GeminiMessagesCompatService{tokenProvider: &GeminiTokenProvider{}, httpUpstream: stub, cfg: &config.Config{}}
					account := &Account{ID: 101, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "test-token", "project_id": "project-test"}}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					request := map[string]any{"model": "gemini-2.5-flash", "stream": stream, "max_tokens": 128, "messages": []any{map[string]any{"role": "user", "content": "draw an apple"}}}
					if protocol == "responses" {
						request = map[string]any{"model": "gemini-2.5-flash", "stream": stream, "input": "draw an apple"}
					}
					body, err := json.Marshal(request)
					require.NoError(t, err)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, strings.NewReader(string(body)))
					var result *ForwardResult
					switch protocol {
					case "messages":
						result, err = svc.Forward(context.Background(), c, account, body)
					case "chat":
						result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body)
					case "responses":
						result, err = svc.ForwardAsResponses(context.Background(), c, account, body)
					}
					require.Equal(t, 1, stub.calls)
					require.NotContains(t, rec.Body.String(), "![image]")
					require.Zero(t, observedGeminiImageOutputs(c))
					if blocked {
						require.NoError(t, err)
						require.Equal(t, http.StatusOK, rec.Code)
						require.Equal(t, 10, result.Usage.InputTokens)
						require.Zero(t, result.Usage.OutputTokens)
						require.Zero(t, result.ImageCount)
						signals := GetOpsStreamErrors(c)
						require.Len(t, signals, 1)
						require.Equal(t, "SAFETY", signals[0].Code)
						require.True(t, signals[0].RequestScoped)
						require.False(t, signals[0].CountTowardsSLA)
						require.Equal(t, !stream, signals[0].NonStream)
					} else {
						require.Error(t, err)
						require.Empty(t, GetOpsStreamErrors(c))
						if stream {
							require.Equal(t, 10, result.Usage.InputTokens)
						} else {
							require.Nil(t, result)
							require.Equal(t, http.StatusBadGateway, rec.Code)
						}
					}
				})
			}
		}
	}
}

func TestCollectGeminiSSEPolicyBlockIsTerminal(t *testing.T) {
	for _, reason := range []string{"SAFETY", "BLOCKED_REASON_UNSPECIFIED"} {
		payload := `{"promptFeedback":{"blockReason":"` + reason + `"},"usageMetadata":{"promptTokenCount":10}}`
		collected, usage, _, err := collectGeminiSSE(strings.NewReader("data: "+payload+"\n\n"), false)
		require.Equal(t, 10, usage.InputTokens)
		if reason == "SAFETY" {
			require.NoError(t, err)
			raw, err := json.Marshal(collected)
			require.NoError(t, err)
			signal, ok := detectGeminiResponseSignal(raw)
			require.True(t, ok)
			require.Equal(t, geminiSignalPromptBlocked, signal.Kind)
			require.Equal(t, "SAFETY", signal.Reason)
			require.Empty(t, geminiInlineImageOutputs(raw))
		} else {
			require.ErrorContains(t, err, "missing terminal event")
		}
	}
}

func TestGeminiForwardNativeBufferedSafetyEOFIsRequestScoped(t *testing.T) {
	// A valid Google prompt block has no candidates, finishReason or [DONE].
	// OAuth generateContent still consumes upstream SSE before returning JSON.
	payload := `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":10}}`
	stub := &geminiCompatHTTPUpstreamStub{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: " + payload + "\n\n")),
	}}
	svc := &GeminiMessagesCompatService{tokenProvider: &GeminiTokenProvider{}, httpUpstream: stub, cfg: &config.Config{}}
	account := &Account{ID: 101, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "test-token", "project_id": "project-test"}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw an apple"}]}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(string(body)))
	result, err := svc.ForwardNative(context.Background(), c, account, "gemini-2.5-flash", "generateContent", false, body)
	require.NoError(t, err)
	require.Equal(t, 1, stub.calls)
	require.Contains(t, stub.lastReq.URL.String(), ":streamGenerateContent")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.JSONEq(t, payload, rec.Body.String())
	require.False(t, result.Stream)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
	require.Zero(t, result.ImageCount)
	signals := GetOpsStreamErrors(c)
	require.Len(t, signals, 1)
	require.Equal(t, "SAFETY", signals[0].Code)
	require.True(t, signals[0].RequestScoped)
	require.True(t, signals[0].NonStream)
	require.False(t, signals[0].CountTowardsSLA)
	require.Empty(t, upstreamErrorEventsFromContext(t, c))
}

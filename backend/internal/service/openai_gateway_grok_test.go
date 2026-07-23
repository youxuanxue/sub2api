//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPatchGrokResponsesBodySetsMappedModelAndDropsUnsupportedFields(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok",
		"input": "hello",
		"prompt_cache_retention": "24h",
		"safety_identifier": "user-1",
		"reasoning": {"effort": "high"}
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.3")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))
	require.Equal(t, "grok-4.3", gjson.GetBytes(patched, "model").String())
	require.False(t, gjson.GetBytes(patched, "prompt_cache_retention").Exists())
	require.False(t, gjson.GetBytes(patched, "safety_identifier").Exists())
	require.Equal(t, "high", gjson.GetBytes(patched, "reasoning.effort").String())
}

func TestPatchGrokResponsesBodySanitizesComposerReasoningParameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		upstreamModel string
		wantReasoning bool
	}{
		{name: "composer fast", upstreamModel: "grok-composer-2.5-fast"},
		{name: "composer shorthand", upstreamModel: "grok-composer"},
		{name: "composer legacy alias", upstreamModel: "composer-2.5"},
		{name: "provider-prefixed composer", upstreamModel: "xai/grok-composer-2.5-fast"},
		{name: "grok 4.5", upstreamModel: "grok-4.5", wantReasoning: true},
	}

	body := []byte(`{
		"model": "grok",
		"input": "hello",
		"reasoning": {"effort": "medium", "summary": "auto"},
		"reasoning_effort": "medium",
		"reasoningEffort": "medium"
	}`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patched, err := patchGrokResponsesBody(body, tt.upstreamModel)
			require.NoError(t, err)
			require.True(t, json.Valid(patched))
			require.Equal(t, tt.upstreamModel, gjson.GetBytes(patched, "model").String())

			if tt.wantReasoning {
				require.Equal(t, "medium", gjson.GetBytes(patched, "reasoning.effort").String())
				require.Equal(t, "medium", gjson.GetBytes(patched, "reasoning_effort").String())
				require.Equal(t, "medium", gjson.GetBytes(patched, "reasoningEffort").String())
				return
			}

			require.False(t, gjson.GetBytes(patched, "reasoning").Exists())
			require.False(t, gjson.GetBytes(patched, "reasoning_effort").Exists())
			require.False(t, gjson.GetBytes(patched, "reasoningEffort").Exists())
		})
	}
}

func TestExtractGrokResponsesReasoningEffortSupportsOpenAICompatibleField(t *testing.T) {
	t.Parallel()

	effort := extractOpenAIReasoningEffortFromBody(
		[]byte(`{"model":"grok-4.3","reasoning_effort":"high"}`),
		"grok-4.3",
	)
	require.NotNil(t, effort)
	require.Equal(t, "high", *effort)
}

func TestPatchGrokResponsesBodyDropsGrok45ReasoningUnsupportedFields(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok-latest",
		"input": "hello",
		"presence_penalty": 0.1,
		"presencePenalty": 0.2,
		"frequency_penalty": 0.3,
		"frequencyPenalty": 0.4,
		"stop": ["done"]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))
	require.Equal(t, "grok-4.5", gjson.GetBytes(patched, "model").String())
	require.False(t, gjson.GetBytes(patched, "presence_penalty").Exists())
	require.False(t, gjson.GetBytes(patched, "presencePenalty").Exists())
	require.False(t, gjson.GetBytes(patched, "frequency_penalty").Exists())
	require.False(t, gjson.GetBytes(patched, "frequencyPenalty").Exists())
	require.False(t, gjson.GetBytes(patched, "stop").Exists())
}

func TestPatchGrokResponsesBodyKeepsPenaltyAndStopFieldsForNon45Models(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok-4.3",
		"input": "hello",
		"presence_penalty": 0.1,
		"frequency_penalty": 0.2,
		"stop": ["done"]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.3")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))
	require.Equal(t, "grok-4.3", gjson.GetBytes(patched, "model").String())
	require.Equal(t, 0.1, gjson.GetBytes(patched, "presence_penalty").Float())
	require.Equal(t, 0.2, gjson.GetBytes(patched, "frequency_penalty").Float())
	require.Len(t, gjson.GetBytes(patched, "stop").Array(), 1)
}

func TestPatchGrokResponsesBodyDropsNestedUnsupportedFields(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok",
		"input": "hello",
		"external_web_access": true,
		"tools": [
			{"type": "function", "name": "kept_fn", "external_web_access": true, "parameters": {"type": "object", "properties": {"q": {"type": "string", "external_web_access": true}}}}
		],
		"metadata": {"external_web_access": false}
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.3")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))
	require.False(t, strings.Contains(string(patched), "external_web_access"))
	require.Equal(t, "kept_fn", gjson.GetBytes(patched, "tools.0.name").String())
}

func TestPatchGrokResponsesBodyDropsUnsupportedNamespaceTools(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok",
		"input": "hello",
		"tools": [
			{"type": "namespace", "namespace": "functions", "tools": [{"type": "function", "name": "inner"}]},
			{"type": "function", "name": "kept_fn", "parameters": {"type": "object"}},
			{"type": "shell", "name": "kept_shell"}
		],
		"tool_choice": {"type": "function", "name": "kept_fn"}
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.3")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))
	require.Equal(t, "grok-4.3", gjson.GetBytes(patched, "model").String())
	require.Len(t, gjson.GetBytes(patched, "tools").Array(), 2)
	require.False(t, gjson.GetBytes(patched, `tools.#(type=="namespace")`).Exists())
	require.True(t, gjson.GetBytes(patched, `tools.#(type=="function")`).Exists())
	require.True(t, gjson.GetBytes(patched, `tools.#(type=="shell")`).Exists())
	require.Equal(t, "kept_fn", gjson.GetBytes(patched, "tool_choice.name").String())
}

func TestPatchGrokResponsesBodyDropsToolChoiceWhenNoSupportedToolsRemain(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok",
		"input": "hello",
		"tools": [
			{"type": "namespace", "namespace": "functions"},
			{"type": "image_generation", "model": "gpt-image-2"}
		],
		"tool_choice": {"type": "namespace", "namespace": "functions"}
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.3")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))
	require.False(t, gjson.GetBytes(patched, "tools").Exists())
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())
}

func TestBuildGrokResponsesRequestUsesResolvedOAuthTargetURL(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")

	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url": "https://xai.test/v1/",
		},
	}
	targetURL, err := svc.resolveGrokResponsesUpstream(account)
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/responses", targetURL)

	req, err := buildGrokResponsesRequestForAccount(context.Background(), nil, account, targetURL, []byte(`{"model":"grok-4.3"}`), "access-token", "")
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, "https://xai.test/v1/responses", req.URL.String())
	require.Equal(t, "Bearer access-token", req.Header.Get("Authorization"))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Contains(t, req.Header.Get("Accept"), "text/event-stream")
	require.Equal(t, grokCLIVersion, req.Header.Get("X-Grok-Client-Version"))
	require.Empty(t, req.Header.Get(grokConversationIDHeader))

	data, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, `{"model":"grok-4.3"}`, strings.TrimSpace(string(data)))
}

func TestBuildGrokResponsesRequestRejectsEmptyTargetURL(t *testing.T) {
	t.Parallel()

	account := healthyGrokOAuthGatewayTestAccount(1, "access-token")
	_, err := buildGrokResponsesRequestForAccount(context.Background(), nil, account, " ", []byte(`{"model":"grok-4.3"}`), "access-token", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "target URL is empty")
}

func TestResolveGrokResponsesUpstreamRejectsUnsafeRelayBaseURL(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
		Enabled:       true,
		UpstreamHosts: []string{"api-us4.tokenkey.dev", "api.x.ai"},
	}}}}
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "edge-grok-key",
			"base_url": "https://evil.example.com",
		},
	}

	_, err := svc.resolveGrokResponsesUpstream(account)
	require.Error(t, err)
	require.Contains(t, err.Error(), "host is not allowed")
}

func TestGrokMediaGenerationGateCoversImagesAndVideo(t *testing.T) {
	tests := []struct {
		name     string
		endpoint GrokMediaEndpoint
		want     bool
	}{
		{name: "image generation", endpoint: GrokMediaEndpointImagesGenerations, want: true},
		{name: "image edit", endpoint: GrokMediaEndpointImagesEdits, want: true},
		{name: "video generation", endpoint: GrokMediaEndpointVideosGenerations, want: true},
		{name: "video status", endpoint: GrokMediaEndpointVideoStatus, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.endpoint.IsGenerationRequest())
		})
	}
}

func TestExtractGrokMediaModelSupportsJSONAndMultipart(t *testing.T) {
	require.Equal(t, "grok-imagine", ExtractGrokMediaModel("application/json", []byte(`{"model":"grok-imagine"}`)))

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("prompt", "draw a cat"))
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	require.NoError(t, writer.Close())

	require.Equal(t, "grok-imagine-edit", ExtractGrokMediaModel(writer.FormDataContentType(), buf.Bytes()))
}

func TestParseGrokMediaRequestBuildsMultipartModerationBody(t *testing.T) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("prompt", "edit this private image"))
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", `form-data; name="image"; filename="input.png"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(partHeader)
	require.NoError(t, err)
	_, err = part.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	info := ParseGrokMediaRequest(writer.FormDataContentType(), buf.Bytes())
	require.Equal(t, "grok-imagine-edit", info.Model)
	require.Equal(t, "edit this private image", info.Prompt)

	moderationBody := info.ModerationBody()
	require.NotEmpty(t, moderationBody)
	require.Equal(t, "edit this private image", gjson.GetBytes(moderationBody, "prompt").String())
	require.True(t, strings.HasPrefix(gjson.GetBytes(moderationBody, "images.0.image_url").String(), "data:image/"))
}

func TestParseGrokMediaVideoRequestResolution(t *testing.T) {
	info := ParseGrokMediaRequest("application/json", []byte(`{"model":"grok-imagine-video","prompt":"waves","resolution":"720p"}`))

	require.Equal(t, "grok-imagine-video", info.Model)
	require.Equal(t, "720p", info.Resolution)
}

func TestNormalizeGrokMediaModelForEndpoint(t *testing.T) {
	tests := []struct {
		name          string
		endpoint      GrokMediaEndpoint
		model         string
		hasInputImage bool
		want          string
	}{
		{name: "image generation alias", endpoint: GrokMediaEndpointImagesGenerations, model: "grok-imagine", want: "grok-imagine-image-quality"},
		{name: "image edit alias", endpoint: GrokMediaEndpointImagesEdits, model: "grok-imagine", want: "grok-imagine-image-quality"},
		{name: "image quality passthrough", endpoint: GrokMediaEndpointImagesGenerations, model: "grok-imagine-image-quality", want: "grok-imagine-image-quality"},
		{name: "image fast passthrough", endpoint: GrokMediaEndpointImagesGenerations, model: "grok-imagine-image", want: "grok-imagine-image"},
		{name: "video passthrough", endpoint: GrokMediaEndpointVideosGenerations, model: "grok-imagine-video", want: "grok-imagine-video"},
		{name: "video 1.5 text-only fallback", endpoint: GrokMediaEndpointVideosGenerations, model: "grok-imagine-video-1.5", want: "grok-imagine-video"},
		{name: "video 1.5 image-to-video passthrough", endpoint: GrokMediaEndpointVideosGenerations, model: "grok-imagine-video-1.5", hasInputImage: true, want: "grok-imagine-video-1.5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, NormalizeGrokMediaModelForEndpoint(tt.endpoint, tt.model, tt.hasInputImage))
		})
	}
}

func TestForwardGrokMediaImagesGenerationNormalizesImagineAlias(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          61,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-image-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://images.test/cat.png"}]}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.Equal(t, "Bearer api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.JSONEq(t, `{"model":"grok-imagine-image-quality","prompt":"draw a cat"}`, string(upstream.lastBody))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"data":[{"url":"https://images.test/cat.png"}]}`, recorder.Body.String())
	require.Equal(t, "xai-image-req", result.RequestID)
	require.Equal(t, "grok-imagine-image-quality", result.Model)
	require.Equal(t, "grok-imagine-image-quality", result.BillingModel)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, ImageBillingSize2K, result.ImageSize)
}

func TestForwardGrokMediaAllowsEdgeRelayBaseURLWithAllowlistEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-image","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          65,
		Name:        "grok-us4",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "edge-grok-key",
			"base_url": "https://api-us4.tokenkey.dev",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"url":"https://images.test/cat.png"}]}`)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled:       true,
			UpstreamHosts: []string{"api-us4.tokenkey.dev", "api.x.ai"},
		}}},
		httpUpstream: upstream,
	}

	_, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://api-us4.tokenkey.dev/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer edge-grok-key", upstream.lastReq.Header.Get("Authorization"))
}

func TestForwardGrokMediaImagesGenerationStripsUnsupportedSize(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-image","prompt":"draw a cat","size":"1024x1024"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          65,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://images.test/cat.png"}]}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"grok-imagine-image","prompt":"draw a cat"}`, string(upstream.lastBody))
	require.Equal(t, ImageBillingSize1K, result.ImageSize)
	require.Equal(t, "1024x1024", result.ImageInputSize)
}

func TestForwardGrokMediaImagesEditMultipartConvertsToJSON(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
	require.NoError(t, writer.WriteField("prompt", "edit this private image"))
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", `form-data; name="image"; filename="input.png"`)
	partHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(partHeader)
	require.NoError(t, err)
	_, err = part.Write([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a})
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(buf.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())

	account := &Account{
		ID:          62,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":       "api-key",
			"base_url":      "https://xai.test/v1",
			"model_mapping": map[string]any{"grok-imagine-edit": "vendor-image-edit"},
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://images.test/edited.png"}]}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesEdits, "", buf.Bytes(), writer.FormDataContentType())
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/images/edits", upstream.lastReq.URL.String())
	require.Equal(t, "application/json", upstream.lastReq.Header.Get("Content-Type"))
	require.True(t, json.Valid(upstream.lastBody))
	require.Equal(t, "vendor-image-edit", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "edit this private image", gjson.GetBytes(upstream.lastBody, "prompt").String())
	require.True(t, strings.HasPrefix(gjson.GetBytes(upstream.lastBody, "image.url").String(), "data:image/png;base64,"))
	require.False(t, gjson.GetBytes(upstream.lastBody, "image.image_url").Exists())
	require.Equal(t, "grok-imagine-edit", result.BillingModel)
	require.Equal(t, "vendor-image-edit", result.UpstreamModel)
}

func TestForwardGrokMediaVideoGenerationReturnsUsageAndResponseID(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-video-1.5","prompt":"waves","resolution":"720p","duration":10}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          63,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-video-generate-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"request_id":"video-request-123","usage":{"prompt_tokens":3,"completion_tokens":4}}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/generations", upstream.lastReq.URL.String())
	require.JSONEq(t, `{"model":"grok-imagine-video","prompt":"waves","resolution":"720p","duration":10}`, string(upstream.lastBody))
	require.Equal(t, "video-request-123", result.ResponseID)
	require.Equal(t, "grok-imagine-video", result.BillingModel)
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 1, result.ImageCount)
	require.Empty(t, result.ImageSize)
	require.Equal(t, 1, result.VideoCount)
	require.Equal(t, VideoBillingResolution720P, result.VideoResolution)
	require.Equal(t, 10, result.VideoDurationSeconds)
}

func TestForwardGrokMediaVideoGenerationPreservesImageToVideoModel(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine-video-1.5","prompt":"animate","image":{"image_url":"data:image/png;base64,aW1n"}}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          63,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"request_id":"video-request-456"}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/generations", upstream.lastReq.URL.String())
	require.JSONEq(t, `{"model":"grok-imagine-video-1.5","prompt":"animate","image":{"url":"data:image/png;base64,aW1n"}}`, string(upstream.lastBody))
	require.Equal(t, "video-request-456", result.ResponseID)
	require.Equal(t, "grok-imagine-video-1.5", result.BillingModel)
	// 未指定 duration 时按上游默认 8 秒计费。
	require.Equal(t, VideoBillingDefaultDurationSeconds, result.VideoDurationSeconds)
}

func TestForwardGrokMediaOAuthImageToVideoUsesOfficialAPIForLargeBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	imageData := strings.Repeat("A", 2*1024*1024)
	body := []byte(`{"model":"grok-imagine-video-1.5","prompt":"animate","image":{"image_url":"data:image/png;base64,` + imageData + `"}}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          66,
		Name:        "grok-oauth",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "oauth-access-token",
			"refresh_token": "oauth-refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			"base_url":      xai.DefaultCLIBaseURL,
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"request_id":"video-request-oauth"}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream, grokTokenProvider: NewGrokTokenProvider(nil, nil)}

	_, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideosGenerations, "", body, "application/json")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultBaseURL+"/videos/generations", upstream.lastReq.URL.String())
	require.Empty(t, upstream.lastReq.Header.Get("X-XAI-Token-Auth"))
	require.Empty(t, upstream.lastReq.Header.Get("x-grok-client-version"))
	require.Equal(t, "data:image/png;base64,"+imageData, gjson.GetBytes(upstream.lastBody, "image.url").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "image.image_url").Exists())
}

func TestForwardGrokMediaVideoStatusUsesGETWithoutBody(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/request-123", nil)

	account := &Account{
		ID:          62,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "api-key",
			"base_url": "https://xai.test/v1",
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-video-req"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"request-123","status":"completed"}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointVideoStatus, "request-123", nil, "")
	require.NoError(t, err)
	require.Equal(t, "https://xai.test/v1/videos/request-123", upstream.lastReq.URL.String())
	require.Equal(t, http.MethodGet, upstream.lastReq.Method)
	require.Equal(t, "Bearer api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.NotEqual(t, grokUpstreamUserAgent, upstream.lastReq.Header.Get("User-Agent"))
	require.Empty(t, upstream.lastReq.Header.Get("Content-Type"))
	require.Empty(t, upstream.lastBody)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"id":"request-123","status":"completed"}`, recorder.Body.String())
	require.Equal(t, "xai-video-req", result.RequestID)
}

func TestForwardGrokMediaVideoMutationEndpoints(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		endpoint GrokMediaEndpoint
		path     string
	}{
		{name: "edit", endpoint: GrokMediaEndpointVideosEdits, path: "/videos/edits"},
		{name: "extension", endpoint: GrokMediaEndpointVideosExtensions, path: "/videos/extensions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			body := []byte(`{"model":"grok-imagine-video","prompt":"continue","video":{"url":"https://example.com/in.mp4"},"duration":6}`)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1"+tt.path, bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			account := &Account{
				ID: 71, Name: "grok", Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "api-key", "base_url": "https://xai.test/v1"},
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"request_id":"video-mutation-123"}`)),
			}}
			svc := &OpenAIGatewayService{httpUpstream: upstream}

			result, err := svc.ForwardGrokMedia(context.Background(), c, account, tt.endpoint, "", body, "application/json")
			require.NoError(t, err)
			require.Equal(t, "https://xai.test/v1"+tt.path, upstream.lastReq.URL.String())
			require.Equal(t, http.MethodPost, upstream.lastReq.Method)
			require.JSONEq(t, string(body), string(upstream.lastBody))
			require.Equal(t, "video-mutation-123", result.ResponseID)
			require.Equal(t, 1, result.VideoCount)
			require.Equal(t, 6, result.VideoDurationSeconds)
		})
	}
}

func TestGrokMediaVideoRequestBindingIsScopedToUserAndAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/video-request-123", nil)
	c.Request.Header.Set("session_id", "shared-client-session")
	groupID := int64(7)
	cache := &stubGatewayCache{}
	svc := &OpenAIGatewayService{cache: cache}
	const userID int64 = 41
	const apiKeyID int64 = 51
	require.NotEmpty(t, svc.GenerateExplicitSessionHash(c, nil))
	ctx := c.Request.Context()

	hash := GrokMediaVideoRequestSessionHash("video-request-123", userID, apiKeyID)
	require.NotEmpty(t, hash)
	require.NoError(t, svc.BindGrokMediaVideoRequestAccount(ctx, &groupID, "video-request-123", userID, apiKeyID, 63))

	accountID, err := svc.ResolveGrokMediaVideoRequestAccount(ctx, &groupID, "video-request-123", userID, apiKeyID)
	require.NoError(t, err)
	require.Equal(t, int64(63), accountID)

	accountID, err = svc.ResolveGrokMediaVideoRequestAccount(ctx, &groupID, "video-request-123", userID+1, apiKeyID)
	require.Error(t, err)
	require.Zero(t, accountID)

	accountID, err = svc.ResolveGrokMediaVideoRequestAccount(ctx, &groupID, "video-request-123", userID, apiKeyID+1)
	require.Error(t, err)
	require.Zero(t, accountID)
}

func TestForwardGrokMedia429ReconcilesRateLimitBeforeCustomErrorBypass(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-imagine","prompt":"draw a cat"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          64,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":                    "api-key",
			"base_url":                   "https://xai.test/v1",
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusBadRequest)},
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-error-req"},
			"Retry-After":    []string{"45"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"do not expose this upstream detail"}}`)),
	}}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{httpUpstream: upstream, accountRepo: repo}

	result, err := svc.ForwardGrokMedia(context.Background(), c, account, GrokMediaEndpointImagesGenerations, "", body, "application/json")
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Upstream gateway error")
	require.NotContains(t, recorder.Body.String(), "do not expose")
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestGrokMedia429FailoverPreservesRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	account := &Account{
		ID: 641, Name: "grok-oauth", Platform: PlatformGrok, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusTooManyRequests)},
		},
	}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"45"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}

	result, err := svc.handleGrokMediaErrorResponse(context.Background(), resp, c, account, "request-id", "grok-imagine")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Equal(t, "45", failoverErr.ResponseHeaders.Get("Retry-After"))
}

func healthyGrokOAuthGatewayTestAccount(id int64, token string) *Account {
	return &Account{
		ID:          id,
		Name:        "grok",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  token,
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(2 * grokTokenRefreshSkew).UTC().Format(time.RFC3339),
			"base_url":      xai.DefaultCLIBaseURL,
		},
	}
}

func TestForwardAsChatCompletionsForGrokStopFallsBackToXAIChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok","messages":[{"role":"user","content":"hi"}],"stream":false,"stop":"done","prompt_cache_key":"raw-client-cache-key"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 5101})

	account := healthyGrokOAuthGatewayTestAccount(51, "access-token")
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{51: account},
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"application/json"},
			"Xai-Request-Id":                 []string{"xai-req"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"9"},
			"X-Ratelimit-Limit-Tokens":       []string{"1000"},
			"X-Ratelimit-Remaining-Tokens":   []string{"990"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl","object":"chat.completion","model":"grok-4.3","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":1}}}`)),
	}}
	svc := &OpenAIGatewayService{
		cfg:               rawChatCompletionsTestConfig(),
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.NotEmpty(t, upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.NotEqual(t, "raw-client-cache-key", upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.Equal(t, "grok", result.Model)
	require.Equal(t, "grok-4.5", result.UpstreamModel)
	require.Equal(t, 1, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.NotNil(t, repo.updates[51][grokQuotaSnapshotExtraKey])
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestForwardGrokResponsesStreamingDefaultsEmptyModelTo45AndSnapshots(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"input":"hi","stream":true,"reasoning_effort":"high"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("OpenAI-Beta", "responses=experimental")
	c.Set("api_key", &APIKey{ID: 5201})

	account := healthyGrokOAuthGatewayTestAccount(52, "access-token")
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{52: account},
		},
	}
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_grok","model":"grok-4.3","usage":{"input_tokens":5,"output_tokens":3,"input_tokens_details":{"cached_tokens":2}}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"Xai-Request-Id":                 []string{"xai-stream-req"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"8"},
			"X-Ratelimit-Limit-Tokens":       []string{"1000"},
			"X-Ratelimit-Remaining-Tokens":   []string{"990"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:               rawChatCompletionsTestConfig(),
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "", true, time.Now())
	require.NoError(t, err)
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "responses=experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String(), upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.Equal(t, "web_search", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(upstream.lastBody, "tools.1.type").String())
	require.Equal(t, "none", gjson.GetBytes(upstream.lastBody, "tool_choice").String())
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.True(t, result.Stream)
	require.Equal(t, "resp_grok", result.ResponseID)
	require.Equal(t, "xai-stream-req", result.RequestID)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 2, result.Usage.CacheReadInputTokens)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, recorder.Body.String(), "response.output_text.delta")
	require.NotNil(t, repo.updates[52][grokQuotaSnapshotExtraKey])
}

func TestResolveGrokResponsesUpstreamAPIKeyRelayUsesEdgeOpenAIResponses(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
		Enabled:       true,
		UpstreamHosts: []string{"api.x.ai"},
	}}}}
	account := &Account{
		ID:       65,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "edge-grok-key",
			"base_url": "https://api-us4.tokenkey.dev",
		},
	}

	targetURL, err := svc.resolveGrokResponsesUpstream(account)
	require.NoError(t, err)
	require.Equal(t, "https://api-us4.tokenkey.dev/v1/responses", targetURL)

	token, err := svc.grokResponsesAuthToken(context.Background(), nil, account)
	require.NoError(t, err)
	require.Equal(t, "edge-grok-key", token)
}

func TestForwardGrokResponsesAPIKeyRelayUsesEdgeResponsesURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-4.3","input":"hi","stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	account := &Account{
		ID:          65,
		Name:        "grok-us4",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "edge-grok-key",
			"base_url": "https://api-us4.tokenkey.dev",
		},
	}
	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_edge","model":"grok-4.3","usage":{"input_tokens":3,"output_tokens":2}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.3", true, time.Now())
	require.NoError(t, err)
	require.Equal(t, "https://api-us4.tokenkey.dev/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer edge-grok-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "grok-4.3", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, result.Stream)
	require.Equal(t, "resp_edge", result.ResponseID)
	require.Contains(t, recorder.Body.String(), "response.output_text.delta")
}

func TestForwardAsAnthropic_GrokAPIKeyRelayUsesEdgeChatCompletionsURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"grok-4.3","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	chatSSE := strings.Join([]string{
		`data: {"id":"chatcmpl_grok_relay","object":"chat.completion.chunk","model":"grok-4.3","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		"",
		`data: {"id":"chatcmpl_grok_relay","object":"chat.completion.chunk","model":"grok-4.3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_grok_msgs_relay"}},
		Body:       io.NopCloser(strings.NewReader(chatSSE)),
	}}

	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          65,
		Name:        "grok-us4",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":      "edge-grok-key",
			"access_token": "must-not-be-used",
			"base_url":     "https://api-us4.tokenkey.dev",
		},
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://api-us4.tokenkey.dev/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer edge-grok-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "grok-4.3", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "grok-4.3", result.Model)
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
}

func TestForwardGrokResponsesRejectsUnsupportedAccountType(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
	account := &Account{
		Platform: PlatformGrok,
		Type:     "custom",
	}

	_, err := svc.resolveGrokResponsesUpstream(account)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported for responses forwarding")
}

func TestForwardAsChatCompletionsForGrokStreamingUsesRawXAIChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	// TokenKey forwards the real client User-Agent verbatim (fingerprint
	// preservation), rather than overriding it with a relay-identifying UA.
	c.Request.Header.Set("User-Agent", "grok-cli/1.2.3")

	account := healthyGrokOAuthGatewayTestAccount(53, "access-token")
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{53: account},
		},
	}
	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_grok","object":"chat.completion.chunk","model":"grok-4.3","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		"",
		`data: {"id":"chatcmpl_grok","object":"chat.completion.chunk","model":"grok-4.3","choices":[],"usage":{"prompt_tokens":6,"completion_tokens":4,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":1}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":                   []string{"text/event-stream"},
			"X-Request-Id":                   []string{"chat-stream-req"},
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"7"},
		},
		Body: io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:               rawChatCompletionsTestConfig(),
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
	require.Equal(t, "grok-cli/1.2.3", upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	require.True(t, result.Stream)
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.Contains(t, recorder.Body.String(), "data: [DONE]")
	require.NotNil(t, repo.updates[53][grokQuotaSnapshotExtraKey])
}

func TestForwardAsChatCompletionsForGrokComposerBridgesImageInput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok-composer-2.5-fast","messages":[{"role":"system","content":"You are concise."},{"role":"user","content":[{"type":"text","text":"What is shown?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &APIKey{ID: 5501})

	account := healthyGrokOAuthGatewayTestAccount(55, "access-token")
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{55: account},
		},
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "xai-request-id": []string{"vision-req"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_vision","object":"response","model":"grok-build-0.1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"A small diagram with ABC letters."}]}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":                   []string{"application/json"},
				"X-Request-Id":                   []string{"composer-req"},
				"X-Ratelimit-Limit-Requests":     []string{"10"},
				"X-Ratelimit-Remaining-Requests": []string{"9"},
				"X-Ratelimit-Limit-Tokens":       []string{"1000"},
				"X-Ratelimit-Remaining-Tokens":   []string{"980"},
			},
			Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl_composer","object":"chat.completion","model":"grok-composer-2.5-fast","choices":[{"index":0,"message":{"role":"assistant","content":"It shows ABC."},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`)),
		},
	}}
	svc := &OpenAIGatewayService{
		cfg:               rawChatCompletionsTestConfig(),
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.requests[0].URL.String())
	require.Empty(t, upstream.requests[0].Header.Get(grokConversationIDHeader))
	require.Equal(t, "grok-build-0.1", gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "input_image", gjson.GetBytes(upstream.bodies[0], "input.0.content.1.type").String())
	require.Equal(t, xai.DefaultCLIBaseURL+"/chat/completions", upstream.requests[1].URL.String())
	require.NotEmpty(t, upstream.requests[1].Header.Get(grokConversationIDHeader))
	require.Equal(t, "grok-composer-2.5-fast", gjson.GetBytes(upstream.bodies[1], "model").String())
	require.False(t, strings.Contains(string(upstream.bodies[1]), "image_url"))
	require.Contains(t, gjson.GetBytes(upstream.bodies[1], "messages.1.content").String(), "Image 1 description")
	require.Contains(t, gjson.GetBytes(upstream.bodies[1], "messages.1.content").String(), "A small diagram with ABC letters.")
	require.Equal(t, 14, result.Usage.InputTokens)
	require.Equal(t, 12, result.Usage.OutputTokens)
	require.Equal(t, "It shows ABC.", gjson.Get(recorder.Body.String(), "choices.0.message.content").String())
	require.NotNil(t, repo.updates[55][grokQuotaSnapshotExtraKey])
}

func TestForwardAsAnthropicForGrokUsesXAIResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"grok","max_tokens":32,"stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 5401})
	c.Request.Header.Set("OpenAI-Beta", "grok-experimental")
	c.Request.Header.Set("originator", "opencode")

	account := healthyGrokOAuthGatewayTestAccount(54, "access-token")
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{54: account},
		},
	}
	chatSSE := strings.Join([]string{
		`data: {"id":"chatcmpl_grok_messages","object":"chat.completion.chunk","model":"grok-4.3","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		"",
		`data: {"id":"chatcmpl_grok_messages","object":"chat.completion.chunk","model":"grok-4.3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":3}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(chatSSE)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.Equal(t, xai.DefaultCLIBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "sub2api-grok/1.0", upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, grokCLIVersion, upstream.lastReq.Header.Get("X-Grok-Client-Version"))
	require.Equal(t, "grok-experimental", upstream.lastReq.Header.Get("OpenAI-Beta"))
	require.Empty(t, upstream.lastReq.Header.Get("originator"))
	require.Empty(t, upstream.lastReq.Header.Get("version"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists())
	require.NotEmpty(t, upstream.lastReq.Header.Get(grokConversationIDHeader))
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())
	require.Empty(t, upstream.lastReq.Header.Get("session_id"))
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.NotContains(t, string(upstream.lastBody), "chatgpt.com")
	require.Equal(t, "grok", result.Model)
	require.Equal(t, "grok-4.5", result.UpstreamModel)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, 3, result.Usage.CacheReadInputTokens)
	require.Contains(t, recorder.Body.String(), `"type":"message"`)
	require.Equal(t, int64(3), gjson.Get(recorder.Body.String(), "usage.cache_read_input_tokens").Int())
	require.Contains(t, recorder.Body.String(), "ok")
}

func TestForwardAsAnthropic_GrokNativeSKU_4200309NonReasoning(t *testing.T) {
	testForwardAsAnthropicGrokNativeSKUFallback(t, "grok-4.20-0309-non-reasoning")
}

func TestForwardAsAnthropic_GrokNativeSKU_4200309Reasoning(t *testing.T) {
	testForwardAsAnthropicGrokNativeSKUFallback(t, "grok-4.20-0309-reasoning")
}

func TestForwardAsAnthropic_GrokNativeSKU_Build01(t *testing.T) {
	testForwardAsAnthropicGrokNativeSKUFallback(t, "grok-build-0.1")
}

func TestForwardAsAnthropic_GrokNativeSKU_CodeFast1(t *testing.T) {
	testForwardAsAnthropicGrokNativeSKUFallback(t, "grok-code-fast-1")
}

func testForwardAsAnthropicGrokNativeSKUFallback(t *testing.T, model string) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":32,"stream":false,"messages":[{"role":"user","content":"hello"}]}`, model))
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	chatSSE := strings.Join([]string{
		fmt.Sprintf(`data: {"id":"chatcmpl_grok_native","object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":{"content":"ok"}}]}`, model),
		"",
		fmt.Sprintf(`data: {"id":"chatcmpl_grok_native","object":"chat.completion.chunk","model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`, model),
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{
		responses: []*http.Response{
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_fallback"}},
				Body:       io.NopCloser(strings.NewReader(chatSSE)),
			},
		},
	}
	account := healthyGrokOAuthGatewayTestAccount(91, "access-token")
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{91: account},
		},
	}
	svc := &OpenAIGatewayService{
		cfg:               rawChatCompletionsTestConfig(),
		httpUpstream:      upstream,
		accountRepo:       repo,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 1)
	require.Len(t, upstream.bodies, 1)

	require.Equal(t, xai.DefaultCLIBaseURL+"/chat/completions", upstream.requests[0].URL.String())
	require.Equal(t, model, gjson.GetBytes(upstream.bodies[0], "model").String())
	require.True(t, gjson.GetBytes(upstream.bodies[0], "messages.0").Exists())

	require.Equal(t, model, result.Model)
	require.Equal(t, model, result.UpstreamModel)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"type":"message"`)
	require.Contains(t, recorder.Body.String(), "ok")
}

func TestHandleGrokAccountUpstreamErrorTempUnschedulesReadinessStates(t *testing.T) {
	tests := []struct {
		name            string
		status          int
		headers         http.Header
		wantReason      string
		wantMinCooldown time.Duration
		wantMaxCooldown time.Duration
	}{
		{
			name:            "unauthorized reauth",
			status:          http.StatusUnauthorized,
			wantReason:      "grok credentials unauthorized",
			wantMinCooldown: 10*time.Minute - time.Second,
			wantMaxCooldown: 10*time.Minute + time.Second,
		},
		{
			name:            "forbidden entitlement",
			status:          http.StatusForbidden,
			wantReason:      "grok access or entitlement denied",
			wantMinCooldown: 30*time.Minute - time.Second,
			wantMaxCooldown: 30*time.Minute + time.Second,
		},
		{
			name:            "upstream temporary error",
			status:          http.StatusInternalServerError,
			wantReason:      "grok upstream temporary error",
			wantMinCooldown: 2*time.Minute - time.Second,
			wantMaxCooldown: 2*time.Minute + time.Second,
		},
		{
			name:            "rate limited without retry after uses openai fallback",
			status:          http.StatusTooManyRequests,
			wantReason:      "grok rate limited",
			wantMinCooldown: openAIOAuth429FallbackCooldown - time.Second,
			wantMaxCooldown: openAIOAuth429FallbackCooldown + time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth}
			repo := &grokQuotaAccountRepo{}
			svc := &OpenAIGatewayService{accountRepo: repo}
			before := time.Now()

			svc.handleGrokAccountUpstreamError(context.Background(), account, tt.status, tt.headers, nil)

			require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
			require.Equal(t, 1, repo.tempUnschedCalls)
			require.Zero(t, repo.rateLimitedCalls)
			require.Equal(t, account.ID, repo.lastTempUnschedID)
			require.Equal(t, tt.wantReason, repo.lastTempUnschedReason)
			require.True(t, repo.lastTempUnschedUntil.After(before.Add(tt.wantMinCooldown)))
			require.True(t, repo.lastTempUnschedUntil.Before(before.Add(tt.wantMaxCooldown)))
		})
	}
}

func TestHandleGrokAccountUpstreamErrorUsesConfigured429Fallback(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	settingRepo := newMockSettingRepo()
	data, _ := json.Marshal(RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 12})
	settingRepo.data[SettingKeyRateLimit429CooldownSettings] = string(data)
	rateLimitSvc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimitSvc.SetSettingService(NewSettingService(settingRepo, &config.Config{}))
	svc := &OpenAIGatewayService{accountRepo: repo, rateLimitService: rateLimitSvc}
	account := &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth}

	before := time.Now()
	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, nil)
	after := time.Now()

	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok rate limited", repo.lastTempUnschedReason)
	require.False(t, repo.lastTempUnschedUntil.Before(before.Add(12*time.Second)))
	require.False(t, repo.lastTempUnschedUntil.After(after.Add(12*time.Second)))
}

func TestHandleGrokAccountUpstreamError_DownstreamCapacitySkipsRelayCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	rateLimitSvc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	sat := &fakeOpenAISaturationCounterRL{}
	rateLimitSvc.SetOpenAISaturationCounter(sat)
	svc := &OpenAIGatewayService{
		accountRepo:               repo,
		rateLimitService:          rateLimitSvc,
		tkOpenAISaturationCounter: sat,
	}
	account := grokEdgeStub(80)
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Upstream rate limit exceeded, please retry later"}}`)

	for i := 0; i < 3; i++ {
		svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body, "grok-build-0.1")
	}

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Equal(t, 0, repo.tempCalls, "downstream capacity must not temp-unschedule a prod grok relay stub")
	require.Equal(t, 0, repo.setRateLimitedCalls, "downstream capacity must not whole-account rate-limit a prod grok relay stub")
	require.Equal(t, []int64{80, 80, 80}, sat.incrementIDs)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "grok-build-0.1", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, tkOpenAIMirrorDownstreamEmptyReason, repo.modelRateLimitCalls[0].reason)
}

func TestHandleGrokAccountUpstreamErrorDoesNotShortenExistingPause(t *testing.T) {
	existingUntil := time.Now().Add(15 * time.Minute)
	account := &Account{
		ID:                      64,
		Platform:                PlatformGrok,
		Type:                    AccountTypeOAuth,
		TempUnschedulableUntil:  &existingUntil,
		TempUnschedulableReason: "existing pause",
	}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{"Retry-After": []string{"45"}}, nil)

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, time.Now().Add(45*time.Second), repo.lastRateLimitResetAt, time.Second)
	require.Zero(t, repo.tempUnschedCalls)
	value, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	runtimeUntil, ok := value.(time.Time)
	require.True(t, ok)
	require.WithinDuration(t, existingUntil, runtimeUntil, time.Second)
}

func TestUpdateGrokUsageSnapshotExhaustedSuccessBypassesThrottleAndSetsRateLimited(t *testing.T) {
	account := &Account{ID: 65, Platform: PlatformGrok, Type: AccountTypeOAuth}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{
		accountRepo:           repo,
		codexSnapshotThrottle: newAccountWriteThrottle(time.Hour),
	}
	now := time.Now()

	// Consume the normal snapshot write allowance first.
	svc.updateGrokUsageSnapshot(context.Background(), account, &xai.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Requests: &xai.QuotaWindow{
			Limit:     grokInt64PtrForTest(10),
			Remaining: grokInt64PtrForTest(9),
		},
		UpdatedAt: now.UTC().Format(time.RFC3339),
	})
	resetAt := now.Add(30 * time.Minute).Truncate(time.Second)
	svc.updateGrokUsageSnapshot(context.Background(), account, &xai.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Requests: &xai.QuotaWindow{
			Limit:     grokInt64PtrForTest(10),
			Remaining: grokInt64PtrForTest(0),
			ResetUnix: grokInt64PtrForTest(resetAt.Unix()),
			ResetAt:   resetAt.UTC().Format(time.RFC3339),
		},
		UpdatedAt: now.UTC().Format(time.RFC3339),
	})

	require.Equal(t, 2, repo.updateCalls)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Equal(t, account.ID, repo.lastRateLimitedID)
	require.WithinDuration(t, resetAt, repo.lastRateLimitResetAt, time.Second)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestUpdateGrokUsageSnapshotAvailableSuccessDoesNotSetRateLimited(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 66, Platform: PlatformGrok, Type: AccountTypeOAuth}

	svc.updateGrokUsageSnapshot(context.Background(), account, &xai.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Requests: &xai.QuotaWindow{
			Limit:     grokInt64PtrForTest(10),
			Remaining: grokInt64PtrForTest(1),
		},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	})

	require.Equal(t, 1, repo.updateCalls)
	require.Zero(t, repo.rateLimitedCalls)
}

func TestUpdateGrokUsageFromResponseHeaderlessSuccessClearsObservedCooldown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	limitedAt := now.Add(-grokRateLimitRepeatCooldown)
	observedResetAt := now.Add(-time.Second)
	account := &Account{
		ID:               660,
		Platform:         PlatformGrok,
		Type:             AccountTypeOAuth,
		RateLimitedAt:    &limitedAt,
		RateLimitResetAt: &observedResetAt,
	}
	repo := &grokQuotaAccountRepo{recoveryClearResult: true}
	svc := &OpenAIGatewayService{
		accountRepo:           repo,
		codexSnapshotThrottle: newAccountWriteThrottle(time.Hour),
	}

	svc.updateGrokUsageFromResponse(context.Background(), account, nil, http.StatusOK)

	require.Zero(t, repo.updateCalls, "headerless success must not overwrite an informative quota snapshot")
	require.Equal(t, 1, repo.recoveryClearCalls)
	require.Equal(t, limitedAt, repo.recoveryObservedAt)
	require.Equal(t, observedResetAt, repo.recoveryObservedReset)
	require.Same(t, &observedResetAt, account.RateLimitResetAt, "shared account snapshots must not be mutated in place")
}

func TestUpdateGrokUsageFromResponseRecoveryRespectsCancellationAndAPIKeyBoundary(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	observedResetAt := now.Add(-time.Second)
	observedLimitedAt := observedResetAt.Add(-grokRateLimitRepeatCooldown)

	t.Run("parent cancellation does not mutate account state", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		account := &Account{
			ID:               661,
			Platform:         PlatformGrok,
			Type:             AccountTypeOAuth,
			RateLimitedAt:    &observedLimitedAt,
			RateLimitResetAt: &observedResetAt,
		}
		repo := &grokQuotaAccountRepo{recoveryClearResult: true}
		svc := &OpenAIGatewayService{accountRepo: repo}

		svc.updateGrokUsageFromResponse(ctx, account, nil, http.StatusOK)

		require.Zero(t, repo.recoveryClearCalls)
	})

	t.Run("API key success does not alter OAuth cooldown state", func(t *testing.T) {
		account := &Account{
			ID:               662,
			Platform:         PlatformGrok,
			Type:             AccountTypeAPIKey,
			RateLimitedAt:    &observedLimitedAt,
			RateLimitResetAt: &observedResetAt,
		}
		repo := &grokQuotaAccountRepo{recoveryClearResult: true}
		svc := &OpenAIGatewayService{accountRepo: repo}

		svc.updateGrokUsageFromResponse(context.Background(), account, nil, http.StatusOK)

		require.Zero(t, repo.recoveryClearCalls)
	})
}

func TestUpdateGrokUsageSnapshotExhaustedSuccessWithoutResetUsesFallback(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 67, Platform: PlatformGrok, Type: AccountTypeOAuth}
	before := time.Now()

	svc.updateGrokUsageSnapshot(context.Background(), account, &xai.QuotaSnapshot{
		StatusCode: http.StatusOK,
		Tokens: &xai.QuotaWindow{
			Limit:     grokInt64PtrForTest(2_000_000),
			Remaining: grokInt64PtrForTest(0),
		},
		UpdatedAt: before.UTC().Format(time.RFC3339),
	})

	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, before.Add(grokRateLimitFallbackCooldown), repo.lastRateLimitResetAt, time.Second)
	stored, ok := repo.updates[account.ID][grokQuotaSnapshotExtraKey].(*xai.QuotaSnapshot)
	require.True(t, ok)
	require.NotNil(t, stored.Tokens.ResetUnix)
	paused, _ := shouldAutoPauseGrokQuotaWindow("tokens", stored.Tokens, before.Add(time.Second))
	require.True(t, paused)
	paused, _ = shouldAutoPauseGrokQuotaWindow("tokens", stored.Tokens, repo.lastRateLimitResetAt.Add(time.Second))
	require.False(t, paused)
}

func TestOpenAIWSHTTPBridgeGrok429PersistsRateLimit(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"45"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	svc := &OpenAIGatewayService{accountRepo: repo, httpUpstream: upstream}
	account := &Account{ID: 68, Platform: PlatformGrok, Type: AccountTypeOAuth, Concurrency: 1}
	before := time.Now()

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), nil, account, "token",
		[]byte(`{"type":"response.create","model":"grok-4.3","input":"hi"}`),
		64, "grok-4.3", "", "", "", "cache-id", 1,
		func([]byte) error { return nil },
	)

	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, before.Add(45*time.Second), repo.lastRateLimitResetAt, time.Second)
	require.Zero(t, repo.tempUnschedCalls)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func grokMessagesSSECompletedResponse(responseID string, cachedTokens int) *http.Response {
	body := strings.Join([]string{
		fmt.Sprintf(`data: {"type":"response.completed","response":{"id":%q,"object":"response","model":"grok-4.3","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7,"input_tokens_details":{"cached_tokens":%d}}}}`, responseID, cachedTokens),
		"",
		"data: [DONE]",
		"",
	}, "\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestOpenAIWSHTTPBridgeGrokExhaustedSuccessPersistsRateLimit(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	resetAt := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)
	resp := grokMessagesSSECompletedResponse("resp_ws_limited", 0)
	resp.Header.Set("X-Ratelimit-Limit-Requests", "10")
	resp.Header.Set("X-Ratelimit-Remaining-Requests", "0")
	resp.Header.Set("X-Ratelimit-Reset-Requests", fmt.Sprintf("%d", resetAt.Unix()))
	upstream := &httpUpstreamRecorder{resp: resp}
	svc := &OpenAIGatewayService{accountRepo: repo, httpUpstream: upstream}
	account := &Account{ID: 69, Platform: PlatformGrok, Type: AccountTypeOAuth, Concurrency: 1}

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), nil, account, "token",
		[]byte(`{"type":"response.create","model":"grok-4.3","input":"hi"}`),
		64, "grok-4.3", "", "", "", "cache-id", 1,
		func([]byte) error { return nil },
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, resetAt, repo.lastRateLimitResetAt, time.Second)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestFailoverOpenAIUpstreamHTTPErrorUsesOnlyGrokRateLimitPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 70, Platform: PlatformGrok, Type: AccountTypeOAuth}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"45"}},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	failoverErr := svc.failoverOpenAIUpstreamHTTPError(
		context.Background(), c, account, resp,
		[]byte(`{"error":{"message":"rate limited"}}`), "rate limited", "grok-4.3",
	)

	require.NotNil(t, failoverErr)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestPatchGrokResponsesBody_StripsReasoningContentNull(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok-latest",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking..."}],"content":null,"encrypted_content":null},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello!"}]}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)
	require.True(t, json.Valid(patched))

	input := gjson.GetBytes(patched, "input")
	require.True(t, input.IsArray())

	items := input.Array()
	require.Len(t, items, 3)

	reasoning := items[1]
	require.Equal(t, "reasoning", reasoning.Get("type").String())
	require.True(t, reasoning.Get("summary").Exists(), "summary should be preserved")
	require.False(t, reasoning.Get("content").Exists(), "content: null should be stripped")
}

func TestPatchGrokResponsesBody_KeepsReasoningContentNonNull(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok-latest",
		"input": [
			{"type":"reasoning","summary":[{"type":"summary_text","text":"ok"}],"content":"real content"}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)

	reasoning := gjson.GetBytes(patched, "input.0")
	require.Equal(t, "real content", reasoning.Get("content").String(), "non-null content must not be stripped")
}

func TestPatchGrokResponsesBody_MultipleReasoningContentNull(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "grok-latest",
		"input": [
			{"type":"reasoning","summary":[{"type":"summary_text","text":"r1"}],"content":null},
			{"type":"message","role":"user","content":"hi"},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"r2"}],"content":null}
		]
	}`)

	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	require.NoError(t, err)

	items := gjson.GetBytes(patched, "input").Array()
	require.Len(t, items, 3)

	require.False(t, items[0].Get("content").Exists())
	require.False(t, items[2].Get("content").Exists())
}

func TestIsGrokImageGenerationModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
		want  bool
	}{
		{"grok-imagine", true},
		{"grok-imagine-image-quality", true},
		{"grok-imagine-edit", true},
		{"grok-imagine-image-hd", true},
		{"grok-4.5", false},
		{"grok-composer", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokImageGenerationModel(tt.model))
		})
	}
}

func TestForwardGrokResponsesEntitlement403QuarantinesWithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"input":"hi","model":"grok-build-0.1","stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 8802})

	account := healthyGrokOAuthGatewayTestAccount(88, "access-token")
	repo := &grokQuotaAccountRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{88: account},
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Xai-Request-Id": []string{"xai-responses-entitlement-403"},
		},
		Body: io.NopCloser(strings.NewReader(
			`{"code":"forbidden","error":"You have either run out of available resources or do not have an active Grok subscription."}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:               rawChatCompletionsTestConfig(),
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-build-0.1", false, time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "entitlement 403 must be terminal for responses")
	require.Equal(t, 1, repo.tempUnschedCalls)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Equal(t, "permission_error", gjson.Get(recorder.Body.String(), "error.type").String())
}

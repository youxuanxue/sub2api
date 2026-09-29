package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func directImagesTestAccount() *Account {
	return &Account{ID: 35, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}}
}

// openAIImagesFixturePNG1x1 is a minimal valid 1×1 PNG (base64).
const openAIImagesFixturePNG1x1 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func openAIImagesResponsesSSEFixture() *http.Response {
	body := "" +
		"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"usage\":{\"input_tokens\":5,\"output_tokens\":20,\"output_tokens_details\":{\"image_tokens\":20}},\"tool_usage\":{\"image_gen\":{\"images\":1,\"output_tokens\":20,\"output_tokens_details\":{\"image_tokens\":20}}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"" + openAIImagesFixturePNG1x1 + "\",\"output_format\":\"png\",\"size\":\"1024x1024\"}]}}\n\n" +
		"data: [DONE]\n\n"
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestCodexDirectImagesDropsLegacyTkImageContract(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","size":"auto","output_format":"png","tk_image_contract":"exact"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	upstreamBody, target, err := buildOpenAIImagesOAuthPayload(parsed, parsed.Model)
	require.NoError(t, err)
	require.Equal(t, chatgptCodexURL, target)
	require.False(t, gjson.GetBytes(upstreamBody, "tk_image_contract").Exists())
	require.Equal(t, "image_generation", gjson.GetBytes(upstreamBody, "tools.0.type").String())
}

func TestCodexOAuthImagesResponsesRouting(t *testing.T) {
	for _, model := range []string{"gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare-2026-09-08", "gpt-image-2.5-sunburst-2026-09-08"} {
		t.Run(model, func(t *testing.T) {
			require.True(t, codexDirectImagesModelFamilies(model))
			require.False(t, usesCodexDirectImages(model))
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"  原样保留 prompt  ","quality":"max","size":"auto","response_format":"url","extra":{"preserve":true}}`, model))
			c, rec := newOpenAIImagesTestContext(t, body)
			upstream := &httpUpstreamRecorder{resp: openAIImagesResponsesSSEFixture()}
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			result, err := svc.ForwardImages(ctx, c, directImagesTestAccount(), body, parsed, "")
			require.NoError(t, err)
			require.Equal(t, 1, result.ImageCount)
			require.Equal(t, 20, result.Usage.ImageOutputTokens)
			require.Equal(t, model, result.UpstreamModel)
			require.Equal(t, "/backend-api/codex/responses", result.UpstreamEndpoint)
			require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
			require.NoError(t, upstream.lastReq.Context().Err())
			require.Equal(t, "Bearer test-token", upstream.lastReq.Header.Get("Authorization"))
			require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("Accept"))
			require.Empty(t, upstream.lastReq.Header.Get("OpenAI-Beta"))
			require.NotEmpty(t, upstream.lastReq.Header.Get("Originator"))
			require.NotEmpty(t, upstream.lastReq.Header.Get("User-Agent"))
			require.Equal(t, openAIImagesResponsesMainModel, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
			require.Equal(t, "image_generation", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
			require.Equal(t, "generate", gjson.GetBytes(upstream.lastBody, "tools.0.action").String())
			require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "tools.0.quality").String())
			require.Equal(t, "  原样保留 prompt  ", gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, "extra").Exists())
			require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
			url := gjson.GetBytes(rec.Body.Bytes(), "data.0.url").String()
			require.True(t, strings.HasPrefix(url, "data:image/png;base64,"))
			require.Greater(t, len(url), len("data:image/png;base64,"))
		})
	}
}

func TestCodexOAuthImagesMappingBeforeRouting(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(accountType, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-1","prompt":"draw"}`)
			c, _ := newOpenAIImagesTestContext(t, body)
			upstream := &httpUpstreamRecorder{resp: openAIImagesResponsesSSEFixture()}
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			account := directImagesTestAccount()
			account.Type = accountType
			account.Credentials["model_mapping"] = map[string]any{"gpt-image-2": "gpt-image-2.5-flare"}
			result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "gpt-image-2")
			require.NoError(t, err)
			require.Equal(t, "gpt-image-2.5-flare", result.UpstreamModel)
			require.Equal(t, "gpt-image-2", result.Model)
			require.Equal(t, openAIImagesResponsesMainModel, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "gpt-image-2.5-flare", gjson.GetBytes(upstream.lastBody, "tools.0.model").String())
		})
	}
	for _, model := range []string{"gpt-image-1", "gpt-image-future"} {
		body, target, err := buildOpenAIImagesOAuthPayload(&OpenAIImagesRequest{Prompt: "draw"}, model)
		require.NoError(t, err)
		require.Equal(t, chatgptCodexURL, target)
		require.Equal(t, "gpt-5.6-luna", gjson.GetBytes(body, "model").String())
		require.Equal(t, model, gjson.GetBytes(body, "tools.0.model").String())
	}
}

func TestUsesCodexDirectImagesDisabledForCodex2APIParity(t *testing.T) {
	for _, model := range []string{"gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		require.False(t, usesCodexDirectImages(model), model)
		require.True(t, codexDirectImagesModelFamilies(model), model)
	}
}

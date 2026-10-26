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

func openAIImagesNativeJSONFixture() *http.Response {
	body := `{"created":1710000000,"data":[{"b64_json":"` + openAIImagesFixturePNG1x1 + `","output_format":"png","size":"1024x1024","quality":"low"}],"usage":{"input_tokens":5,"output_tokens":20,"output_tokens_details":{"image_tokens":20}}}`
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
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
	require.Equal(t, strings.TrimSuffix(chatgptCodexURL, "/responses")+"/images/generations", target)
	require.False(t, gjson.GetBytes(upstreamBody, "tk_image_contract").Exists())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstreamBody, "model").String())
	require.Equal(t, "png", gjson.GetBytes(upstreamBody, "output_format").String())
}

func TestCodexOAuthImagesNativeRouting(t *testing.T) {
	for _, model := range []string{"gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare-2026-09-08", "gpt-image-2.5-sunburst-2026-09-08"} {
		t.Run(model, func(t *testing.T) {
			require.True(t, codexDirectImagesModelFamilies(model))
			require.True(t, usesCodexDirectImages(model))
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"  原样保留 prompt  ","quality":"max","size":"auto","response_format":"url","extra":{"preserve":true}}`, model))
			c, rec := newOpenAIImagesTestContext(t, body)
			upstream := &httpUpstreamRecorder{resp: openAIImagesNativeJSONFixture()}
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
			require.Equal(t, "/backend-api/codex/images/generations", result.UpstreamEndpoint)
			require.Equal(t, strings.TrimSuffix(chatgptCodexURL, "/responses")+"/images/generations", upstream.lastReq.URL.String())
			require.NoError(t, upstream.lastReq.Context().Err())
			require.Equal(t, "Bearer test-token", upstream.lastReq.Header.Get("Authorization"))
			require.Equal(t, "application/json", upstream.lastReq.Header.Get("Accept"))
			require.Empty(t, upstream.lastReq.Header.Get("OpenAI-Beta"))
			require.NotEmpty(t, upstream.lastReq.Header.Get("Originator"))
			require.NotEmpty(t, upstream.lastReq.Header.Get("User-Agent"))
			require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
			text := gjson.GetBytes(upstream.lastBody, "prompt").String()
			require.Contains(t, text, "  原样保留 prompt  ")
			assertOpenAIImagesPerceivedQualityPrompt(t, text, "  原样保留 prompt  ", false)
			require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "quality").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, "extra").Exists())
			require.False(t, gjson.GetBytes(upstream.lastBody, "stream").Exists())
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
			upstream := &httpUpstreamRecorder{resp: openAIImagesNativeJSONFixture()}
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
			require.Equal(t, "gpt-image-2.5-flare", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "/backend-api/codex/images/generations", upstream.lastReq.URL.Path)
		})
	}
	for _, model := range []string{"gpt-image-1", "gpt-image-future"} {
		body, target, err := buildOpenAIImagesOAuthPayload(&OpenAIImagesRequest{Prompt: "draw"}, model)
		require.NoError(t, err)
		require.Equal(t, chatgptCodexURL, target)
		require.Equal(t, openAIImagesResponsesMainModel, gjson.GetBytes(body, "model").String())
		require.Equal(t, model, gjson.GetBytes(body, "tools.0.model").String())
	}
}

func TestUsesCodexDirectImagesEnabledForNativeImageFamilies(t *testing.T) {
	for _, model := range []string{"gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		require.True(t, usesCodexDirectImages(model), model)
		require.True(t, codexDirectImagesModelFamilies(model), model)
	}
}

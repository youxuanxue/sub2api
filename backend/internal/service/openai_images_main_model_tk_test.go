//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyOpenAIImagesStyleGuidance(t *testing.T) {
	require.Equal(t, "draw", applyOpenAIImagesStyleGuidance("draw", ""))
	require.Equal(t, "draw\n\nStyle guidance: vivid", applyOpenAIImagesStyleGuidance("draw", "vivid"))
	require.Equal(t, "Style guidance: vivid", applyOpenAIImagesStyleGuidance("", "vivid"))
	require.Equal(t,
		"draw\n\nStyle guidance: vivid",
		applyOpenAIImagesStyleGuidance("draw\n\nStyle guidance: vivid", "cinematic"),
	)
}

func TestApplyOpenAIImagesStyleGuidanceBeforeAspectRatioMarker(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Model:               "gpt-image-2",
		Prompt:              "a red cube",
		Style:               "vivid",
		AspectRatio:         "16:9",
		ExplicitAspectRatio: true,
	}
	body, err := buildOpenAIImagesResponsesRequest(parsed, "gpt-image-2")
	require.NoError(t, err)
	text := gjson.GetBytes(body, "input.0.content.0.text").String()
	require.Equal(t, "a red cube\n\nStyle guidance: vivid, marker AR=16:9", text)
	require.False(t, gjson.GetBytes(body, "tools.0.style").Exists())
}

func TestOpenAIImagesMainModelCandidatesAndFallback(t *testing.T) {
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL", "")
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL_FALLBACKS", "")
	candidates := openAIImagesMainModelCandidates()
	require.Equal(t, openAIImagesResponsesMainModel, candidates[0])
	require.Contains(t, candidates, "gpt-5.5")
	require.Contains(t, candidates, "gpt-5.6-terra")

	next, ok := nextOpenAIImagesMainModelAfterUnsupported(openAIImagesResponsesMainModel, nil)
	require.True(t, ok)
	require.Equal(t, "gpt-5.5", next)

	tried := map[string]bool{
		strings.ToLower(openAIImagesResponsesMainModel): true,
		"gpt-5.5":       true,
		"gpt-5.6-terra": true,
		"gpt-5.6-sol":   true,
		"gpt-6-astra":   true,
	}
	_, ok = nextOpenAIImagesMainModelAfterUnsupported(openAIImagesResponsesMainModel, tried)
	require.False(t, ok)
}

func TestOpenAIImagesMainModelFallbackEnvOverride(t *testing.T) {
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL", "gpt-5.6-luna")
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL_FALLBACKS", "gpt-5.6-sol, gpt-6-astra")
	candidates := openAIImagesMainModelCandidates()
	require.Equal(t, []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-6-astra"}, candidates)
}

func TestForwardOpenAIImagesOAuth_RetriesOnMainModelUnsupported(t *testing.T) {
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL", "")
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL_FALLBACKS", "")

	png := openAIImagesFixturePNG1x1
	rejectBody := `{"detail":"The '` + openAIImagesResponsesMainModel + `' model is not supported when using Codex with a ChatGPT account."}`
	okSSE := "data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"usage\":{\"input_tokens\":5,\"output_tokens\":20,\"output_tokens_details\":{\"image_tokens\":20}},\"tool_usage\":{\"image_gen\":{\"images\":1,\"output_tokens\":20,\"output_tokens_details\":{\"image_tokens\":20}}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"" + png + "\",\"output_format\":\"png\",\"size\":\"1024x1024\"}]}}\n\n"

	upstream := &httpUpstreamRecorder{
		responses: []*http.Response{
			{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(rejectBody)),
			},
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(okSSE)),
			},
		},
	}
	svc := newOpenAIImagesTestService(upstream)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","size":"1024x1024","output_format":"png"}`)
	c, rec := newOpenAIImagesTestContext(t, body)
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	account := directImagesTestAccount()

	result, err := svc.forwardOpenAIImagesOAuth(context.Background(), c, account, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, openAIImagesResponsesMainModel, gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "gpt-5.5", gjson.GetBytes(upstream.bodies[1], "model").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.bodies[1], "tools.0.model").String())
	require.Equal(t, "1024x1024", gjson.GetBytes(rec.Body.Bytes(), "size").String())
}

func TestCanonicalizeOpenAIImagesSizeField(t *testing.T) {
	require.Equal(t, "1536x864", canonicalizeOpenAIImagesSizeField("1536×864"))
	require.Equal(t, "1536x864", canonicalizeOpenAIImagesSizeField("1536*864"))
	require.Equal(t, "1024x768", canonicalizeOpenAIImagesSizeField("1024X768"))
	require.Equal(t, "auto", canonicalizeOpenAIImagesSizeField("AUTO"))
}

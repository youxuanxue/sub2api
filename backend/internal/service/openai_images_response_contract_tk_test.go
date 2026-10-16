//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTruncateOpenAIImagesResults_PostCollectionCap(t *testing.T) {
	results := []openAIResponsesImageResult{
		{Result: "a", RevisedPrompt: "first"},
		{Result: "b", RevisedPrompt: "redo"},
		{Result: "c"},
	}
	got := truncateOpenAIImagesResults(results, 1)
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0].Result)

	require.Len(t, truncateOpenAIImagesResults(results, 2), 2)
	require.Len(t, truncateOpenAIImagesResults(results[:1], 4), 1)
	require.Len(t, truncateOpenAIImagesResults(nil, 1), 0)
}

func TestPrepareOpenAIImagesAPIResponseResults_EchoStripAndTruncate(t *testing.T) {
	parsed := &OpenAIImagesRequest{
		Model:        "gpt-image-2",
		Quality:      "low",
		Background:   "opaque",
		OutputFormat: "png",
		Size:         "1024x1024",
		N:            1,
	}
	results := []openAIResponsesImageResult{
		{Result: "one", Quality: "auto", Model: "gpt-image-2-codex", RevisedPrompt: "rewrite me", Size: "1254x1254"},
		{Result: "two", Quality: "auto", Model: "gpt-image-2-codex", RevisedPrompt: "redo"},
	}
	meta := openAIResponsesImageResult{Quality: "auto", Model: "gpt-image-2-codex", Size: "1254x1254"}
	got := prepareOpenAIImagesAPIResponseResults(results, &meta, parsed, "gpt-image-2")
	require.Len(t, got, 1)
	require.Equal(t, "one", got[0].Result)
	require.Empty(t, got[0].RevisedPrompt)
	require.Equal(t, "low", meta.Quality)
	require.Equal(t, "gpt-image-2", meta.Model)
	require.Equal(t, "opaque", meta.Background)
	require.Equal(t, "png", meta.OutputFormat)
	require.Equal(t, "1024x1024", meta.Size)

	body, err := buildOpenAIImagesAPIResponse(got, 1, nil, meta, "b64_json")
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(body, "quality").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(body, "model").String())
	require.Equal(t, "1024x1024", gjson.GetBytes(body, "size").String())
	require.False(t, gjson.GetBytes(body, "data.0.revised_prompt").Exists())
	require.Equal(t, float64(1), gjson.GetBytes(body, "data.#").Float())
}

func TestValidateOpenAIImagesInputFidelity_RejectsGPTImage2(t *testing.T) {
	err := validateOpenAIImagesInputFidelity(&OpenAIImagesRequest{
		Model:                 "gpt-image-2",
		InputFidelity:         "high",
		ExplicitInputFidelity: true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_fidelity")
	require.Contains(t, err.Error(), "gpt-image-2")

	require.NoError(t, validateOpenAIImagesInputFidelity(&OpenAIImagesRequest{
		Model:                 "gpt-image-1.5",
		InputFidelity:         "high",
		ExplicitInputFidelity: true,
	}))
	require.NoError(t, validateOpenAIImagesInputFidelity(&OpenAIImagesRequest{
		Model: "gpt-image-2",
	}))
}

func TestParseOpenAIImagesRequest_RejectsGPTImage2InputFidelity(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"keep the bottle","input_fidelity":"high"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	_, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_fidelity")
}

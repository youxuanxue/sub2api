package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func assertOpenAIImagesPerceivedQualityPrompt(t *testing.T, text, original string, edit bool) {
	t.Helper()
	require.Contains(t, text, original)
	require.Contains(t, text, "Primary request:")
	require.Contains(t, text, "Composition/framing:")
	require.Contains(t, text, "Lighting/mood:")
	require.Contains(t, text, "Materials/textures:")
	require.Contains(t, text, "Constraints:")
	require.Contains(t, text, "Avoid:")
	lower := strings.ToLower(text)
	for _, banned := range []string{"8k", "4k", "upscale", "real-esrgan", "resolution", "pixel", "quality:"} {
		require.NotContains(t, lower, banned)
	}
	if edit {
		require.Contains(t, text, "input image")
	} else {
		require.NotContains(t, text, "input image")
	}
	if openAIImagesPromptHasQuotedText(original) {
		require.Contains(t, text, "Text (verbatim):")
	}
}

func TestOpenAIImagesReasoningEffortDefaultsAndOverride(t *testing.T) {
	t.Setenv(openAIImagesReasoningEffortEnv, "")

	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("a ceramic mug on a table"))
	require.Equal(t, "high", openAIImagesReasoningEffortForPrompt(`cup text "SkelOT"`))
	require.Equal(t, "high", openAIImagesReasoningEffortForPrompt("杯身只写“SkelOT”"))

	t.Setenv(openAIImagesReasoningEffortEnv, " XHIGH ")
	require.Equal(t, "xhigh", openAIImagesReasoningEffortForPrompt("plain"))
	require.Equal(t, "xhigh", openAIImagesReasoningEffortForPrompt(`quoted "text"`))

	t.Setenv(openAIImagesReasoningEffortEnv, "none")
	require.Equal(t, "none", openAIImagesReasoningEffortForPrompt(`quoted "text"`))

	t.Setenv(openAIImagesReasoningEffortEnv, "max")
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("plain"))
	require.Equal(t, "high", openAIImagesReasoningEffortForPrompt(`quoted "text"`))
}

func TestApplyOpenAIImagesPerceivedQualityPromptScaffold(t *testing.T) {
	t.Setenv(openAIImagesPromptScaffoldEnv, "")
	t.Setenv(openAIImagesReasoningEffortEnv, "")

	plain := applyOpenAIImagesPerceivedQualityPrompt("a ceramic mug", false)
	assertOpenAIImagesPerceivedQualityPrompt(t, plain, "a ceramic mug", false)
	require.Equal(t, plain, applyOpenAIImagesPerceivedQualityPrompt(plain, false))
	require.NotContains(t, plain, "Text (verbatim):")

	quoted := applyOpenAIImagesPerceivedQualityPrompt("杯身只写“SkelOT”", false)
	assertOpenAIImagesPerceivedQualityPrompt(t, quoted, "杯身只写“SkelOT”", false)
	require.True(t, strings.HasPrefix(quoted, "Primary request: 杯身只写“SkelOT”"))

	spaced := applyOpenAIImagesPerceivedQualityPrompt("  原样保留 prompt  ", false)
	require.Contains(t, spaced, "  原样保留 prompt  ")

	edited := applyOpenAIImagesPerceivedQualityPrompt("replace the backdrop", true)
	assertOpenAIImagesPerceivedQualityPrompt(t, edited, "replace the backdrop", true)

	t.Setenv(openAIImagesPromptScaffoldEnv, "off")
	require.Equal(t, "a ceramic mug", applyOpenAIImagesPerceivedQualityPrompt("a ceramic mug", true))
}

func TestBuildOpenAIImagesResponsesRequest_PerceivedQualityWiring(t *testing.T) {
	t.Setenv(openAIImagesPromptScaffoldEnv, "")
	t.Setenv(openAIImagesReasoningEffortEnv, "")

	plain, err := buildOpenAIImagesResponsesRequest(&OpenAIImagesRequest{
		Endpoint: openAIImagesGenerationsEndpoint,
		Model:    "gpt-image-2",
		Prompt:   "draw a cat",
	}, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, "", gjson.GetBytes(plain, "instructions").String())
	require.Equal(t, "medium", gjson.GetBytes(plain, "reasoning.effort").String())
	require.Equal(t, "auto", gjson.GetBytes(plain, "reasoning.summary").String())
	assertOpenAIImagesPerceivedQualityPrompt(t, gjson.GetBytes(plain, "input.0.content.0.text").String(), "draw a cat", false)
	require.Equal(t, "generate", gjson.GetBytes(plain, "tools.0.action").String())

	quotedPrompt := "杯身只写“SkelOT”，不要增加其他文字。"
	quoted, err := buildOpenAIImagesResponsesRequest(&OpenAIImagesRequest{
		Endpoint: openAIImagesGenerationsEndpoint,
		Model:    "gpt-image-2",
		Prompt:   quotedPrompt,
	}, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, "", gjson.GetBytes(quoted, "instructions").String())
	require.Equal(t, "high", gjson.GetBytes(quoted, "reasoning.effort").String())
	assertOpenAIImagesPerceivedQualityPrompt(t, gjson.GetBytes(quoted, "input.0.content.0.text").String(), quotedPrompt, false)

	t.Setenv(openAIImagesReasoningEffortEnv, "low")
	t.Setenv(openAIImagesPromptScaffoldEnv, "false")
	raw, err := buildOpenAIImagesResponsesRequest(&OpenAIImagesRequest{
		Endpoint:       openAIImagesEditsEndpoint,
		Model:          "gpt-image-2",
		Prompt:         "replace background",
		InputImageURLs: []string{"https://example.com/source.png"},
	}, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, "low", gjson.GetBytes(raw, "reasoning.effort").String())
	require.Equal(t, "", gjson.GetBytes(raw, "instructions").String())
	require.Equal(t, "replace background", gjson.GetBytes(raw, "input.0.content.0.text").String())
	require.Equal(t, "edit", gjson.GetBytes(raw, "tools.0.action").String())
}

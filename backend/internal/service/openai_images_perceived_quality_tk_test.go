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
	if openAIImagesPromptHasShortQuotedSpan(original) {
		require.Contains(t, text, "Text (verbatim):")
	} else {
		require.NotContains(t, text, "Text (verbatim):")
	}
	if edit {
		require.Contains(t, text, "keep the input image's framing")
		require.NotContains(t, text, "clear focal subject")
		require.NotContains(t, text, "muddy or directionless light")
	} else {
		require.Contains(t, text, "does not already specify framing")
		require.NotContains(t, text, "keep the input image's framing")
	}
}

func TestOpenAIImagesReasoningEffortDefaultsAndOverride(t *testing.T) {
	t.Setenv(openAIImagesReasoningEffortEnv, "")

	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("cat"))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt(`"cat"`))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("a ceramic mug on a table"))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt(`cup text "SkelOT"`))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("杯身只写“SkelOT”"))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("标题用「SkelOT」"))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("标题用『SkelOT』"))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt(`a 12" monitor`))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt(`from 12" to 24"`))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt(`he said "hello`))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("只写「SkelOT"))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt(`blank ""`))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt(`"   "`))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("杯身只写\"SkelOT\""))

	label48 := `"` + strings.Repeat("a", 48) + `"`
	label49 := `"` + strings.Repeat("b", 49) + `"`
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("mark "+label48))
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("mark "+label49))

	for _, raw := range []string{" XHIGH ", "x-high", "extra_high", "max"} {
		t.Setenv(openAIImagesReasoningEffortEnv, raw)
		require.Equal(t, "xhigh", openAIImagesReasoningEffortForPrompt("plain"), raw)
		require.Equal(t, "xhigh", openAIImagesReasoningEffortForPrompt(`quoted "text"`), raw)
	}

	for _, effort := range []string{"low", "high"} {
		t.Setenv(openAIImagesReasoningEffortEnv, effort)
		require.Equal(t, effort, openAIImagesReasoningEffortForPrompt("cat"))
		require.Equal(t, effort, openAIImagesReasoningEffortForPrompt(`"cat"`))
	}

	t.Setenv(openAIImagesReasoningEffortEnv, "none")
	require.Equal(t, "none", openAIImagesReasoningEffortForPrompt(`quoted "text"`))

	t.Setenv(openAIImagesReasoningEffortEnv, " medium ")
	require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt(`quoted "text"`))

	for _, raw := range []string{"auto", "turbo", "MAXX"} {
		t.Setenv(openAIImagesReasoningEffortEnv, raw)
		require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("plain"), raw)
		require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt(`quoted "text"`), raw)
		require.Equal(t, "medium", openAIImagesReasoningEffortForPrompt("杯身只写“SkelOT”"), raw)
	}
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
	require.Contains(t, quoted, "Text (verbatim):")

	longQuote := "caption " + `"` + strings.Repeat("c", 49) + `"`
	longScaffold := applyOpenAIImagesPerceivedQualityPrompt(longQuote, false)
	assertOpenAIImagesPerceivedQualityPrompt(t, longScaffold, longQuote, false)
	require.NotContains(t, longScaffold, "Text (verbatim):")

	inch := applyOpenAIImagesPerceivedQualityPrompt(`a 12" monitor on a desk`, false)
	assertOpenAIImagesPerceivedQualityPrompt(t, inch, `a 12" monitor on a desk`, false)
	require.NotContains(t, inch, "Text (verbatim):")

	spaced := applyOpenAIImagesPerceivedQualityPrompt("  原样保留 prompt  ", false)
	require.Contains(t, spaced, "  原样保留 prompt  ")

	edited := applyOpenAIImagesPerceivedQualityPrompt("replace the backdrop", true)
	assertOpenAIImagesPerceivedQualityPrompt(t, edited, "replace the backdrop", true)
	require.Equal(t, edited, applyOpenAIImagesPerceivedQualityPrompt(edited, true))
	require.Equal(t, edited, applyOpenAIImagesPerceivedQualityPrompt(edited, false))

	for _, raw := range []string{"off", "OFF", "false", "0", "no"} {
		t.Setenv(openAIImagesPromptScaffoldEnv, raw)
		require.Equal(t, "a ceramic mug", applyOpenAIImagesPerceivedQualityPrompt("a ceramic mug", true), raw)
	}
	t.Setenv(openAIImagesPromptScaffoldEnv, "yes")
	require.Contains(t, applyOpenAIImagesPerceivedQualityPrompt("a ceramic mug", false), "Primary request:")
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
		Quality:  "high",
	}, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, "", gjson.GetBytes(quoted, "instructions").String())
	require.Equal(t, "medium", gjson.GetBytes(quoted, "reasoning.effort").String())
	assertOpenAIImagesPerceivedQualityPrompt(t, gjson.GetBytes(quoted, "input.0.content.0.text").String(), quotedPrompt, false)
	require.Equal(t, "high", gjson.GetBytes(quoted, "tools.0.quality").String())

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
	require.Equal(t, "https://example.com/source.png", gjson.GetBytes(raw, "input.0.content.1.image_url").String())
}

func TestBuildOpenAIImagesResponsesRequest_EditScaffoldKeepsSourceAndForwardsQuality(t *testing.T) {
	t.Setenv(openAIImagesPromptScaffoldEnv, "")
	t.Setenv(openAIImagesReasoningEffortEnv, "")

	prompt := "把背景换成白墙，杯身文字保持“SkelOT”，不要改杯子本身。"
	body, err := buildOpenAIImagesResponsesRequest(&OpenAIImagesRequest{
		Endpoint:            openAIImagesEditsEndpoint,
		Model:               "gpt-image-2",
		Prompt:              prompt,
		Quality:             "high",
		Size:                "1024x1024",
		Style:               "natural",
		AspectRatio:         "1:1",
		ExplicitAspectRatio: true,
		InputImageURLs:      []string{"https://example.com/source.png"},
		MaskImageURL:        "https://example.com/mask.png",
	}, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, "", gjson.GetBytes(body, "instructions").String())
	require.Equal(t, "medium", gjson.GetBytes(body, "reasoning.effort").String())
	require.Equal(t, "auto", gjson.GetBytes(body, "reasoning.summary").String())
	require.Equal(t, "edit", gjson.GetBytes(body, "tools.0.action").String())
	require.Equal(t, "gpt-image-2", gjson.GetBytes(body, "tools.0.model").String())
	require.Equal(t, "high", gjson.GetBytes(body, "tools.0.quality").String())
	require.Equal(t, "1024x1024", gjson.GetBytes(body, "tools.0.size").String())
	require.False(t, gjson.GetBytes(body, "tools.0.n").Exists())
	require.False(t, gjson.GetBytes(body, "tools.0.style").Exists())
	require.Equal(t, "https://example.com/mask.png", gjson.GetBytes(body, "tools.0.input_image_mask.image_url").String())
	require.Equal(t, "https://example.com/source.png", gjson.GetBytes(body, "input.0.content.1.image_url").String())

	text := gjson.GetBytes(body, "input.0.content.0.text").String()
	assertOpenAIImagesPerceivedQualityPrompt(t, text, prompt, true)
	require.Contains(t, text, "Text (verbatim):")
	require.Greater(t, strings.Index(text, "Style guidance: natural"), strings.Index(text, "Primary request:"))
	require.True(t, strings.HasSuffix(text, "marker AR=1:1"))

	ascii, err := buildOpenAIImagesResponsesRequest(&OpenAIImagesRequest{
		Endpoint:       openAIImagesEditsEndpoint,
		Model:          "gpt-image-2",
		Prompt:         `keep the mug, set the label to "LABEL"`,
		Quality:        "high",
		InputImageURLs: []string{"https://example.com/source.png"},
	}, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, "", gjson.GetBytes(ascii, "instructions").String())
	require.Equal(t, "medium", gjson.GetBytes(ascii, "reasoning.effort").String())
	require.Equal(t, "high", gjson.GetBytes(ascii, "tools.0.quality").String())
	require.Equal(t, "edit", gjson.GetBytes(ascii, "tools.0.action").String())
	asciiText := gjson.GetBytes(ascii, "input.0.content.0.text").String()
	assertOpenAIImagesPerceivedQualityPrompt(t, asciiText, `keep the mug, set the label to "LABEL"`, true)
}

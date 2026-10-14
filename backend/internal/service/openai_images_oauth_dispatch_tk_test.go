//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImagesCodexSoftModelRejectsInputFidelity(t *testing.T) {
	for _, model := range []string{"gpt-image-2", "GPT-IMAGE-2", " gpt-image-2-codex ", "gpt-image-2-codex-2026-10-13"} {
		require.True(t, openAIImagesCodexSoftModelRejectsInputFidelity(model), model)
	}
	for _, model := range []string{"", "gpt-image-1", "gpt-image-1.5", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare", "gpt-image-2-2026-04-21", "dall-e-3"} {
		require.False(t, openAIImagesCodexSoftModelRejectsInputFidelity(model), model)
	}
}

func TestBuildOpenAIImagesResponsesRequest_OmitsInputFidelityOnlyForCodexSoftModel(t *testing.T) {
	for _, tc := range []struct {
		model    string
		fidelity string
		want     bool
	}{
		{model: "gpt-image-2", fidelity: "high", want: false},
		{model: "gpt-image-2", fidelity: "low", want: false},
		{model: "gpt-image-2-codex", fidelity: "high", want: false},
		{model: "gpt-image-2-codex", fidelity: "low", want: false},
		{model: "gpt-image-1.5", fidelity: "high", want: true},
		{model: "gpt-image-2.5-sunburst", fidelity: "low", want: true},
		{model: "dall-e-3", fidelity: "high", want: true},
	} {
		t.Run(tc.model+"/"+tc.fidelity, func(t *testing.T) {
			parsed := &OpenAIImagesRequest{
				Endpoint:       openAIImagesEditsEndpoint,
				Model:          tc.model,
				Prompt:         "recolor the square",
				InputFidelity:  tc.fidelity,
				InputImageURLs: []string{"https://example.com/source.png"},
			}
			body, err := buildOpenAIImagesResponsesRequest(parsed, tc.model)
			require.NoError(t, err)
			require.Equal(t, "edit", gjson.GetBytes(body, "tools.0.action").String())
			if tc.want {
				require.Equal(t, tc.fidelity, gjson.GetBytes(body, "tools.0.input_fidelity").String())
			} else {
				require.False(t, gjson.GetBytes(body, "tools.0.input_fidelity").Exists())
			}
		})
	}
}

func TestOpenAIImagesOAuthDispatchFacts_RecordsEffortMainModelAndScaffold(t *testing.T) {
	t.Setenv(openAIImagesReasoningEffortEnv, "")
	t.Setenv(openAIImagesPromptScaffoldEnv, "")
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL", "")
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL_FALLBACKS", "")

	plain := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "a ceramic mug", InputFidelity: "high"}
	facts := openAIImagesOAuthDispatchFactsFor(plain, "gpt-image-2", "")
	require.Equal(t, "medium", facts.ReasoningEffort)
	require.Equal(t, "auto", facts.EffortSource)
	require.Equal(t, openAIImagesResponsesMainModel, facts.MainModel)
	require.NotContains(t, facts.MainModelFallbacks, openAIImagesResponsesMainModel)
	require.Equal(t, openAIImagesMainModelCandidates()[1:], facts.MainModelFallbacks)
	require.Contains(t, facts.MainModelFallbacks, "gpt-6.1-sol")
	require.True(t, facts.ScaffoldOn)
	require.True(t, facts.InputFidelityOmitted)
	require.Equal(t, "high", facts.ClientInputFidelity)

	quoted := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: `cup text "NOVA"`}
	quotedFacts := openAIImagesOAuthDispatchFactsFor(quoted, "gpt-image-2", "")
	require.Equal(t, "medium", quotedFacts.ReasoningEffort)
	require.Equal(t, "auto", quotedFacts.EffortSource)
	require.False(t, quotedFacts.InputFidelityOmitted)

	kept := openAIImagesOAuthDispatchFactsFor(
		&OpenAIImagesRequest{Model: "gpt-image-1.5", Prompt: "draw", InputFidelity: "low"},
		"gpt-image-1.5",
		"",
	)
	require.False(t, kept.InputFidelityOmitted)
	require.Equal(t, "low", kept.ClientInputFidelity)

	t.Setenv(openAIImagesReasoningEffortEnv, "xhigh")
	t.Setenv(openAIImagesPromptScaffoldEnv, "off")
	overridden := openAIImagesOAuthDispatchFactsFor(quoted, "gpt-image-2", "gpt-6.1-sol")
	require.Equal(t, "xhigh", overridden.ReasoningEffort)
	require.Equal(t, "env", overridden.EffortSource)
	require.Equal(t, "gpt-6.1-sol", overridden.MainModel)
	require.NotContains(t, overridden.MainModelFallbacks, "gpt-6.1-sol")
	require.NotContains(t, overridden.MainModelFallbacks, openAIImagesResponsesMainModel)
	require.False(t, overridden.ScaffoldOn)
}

func TestForwardOpenAIImagesOAuth_PersistsDispatchEffortAndStripsCodexFidelity(t *testing.T) {
	t.Setenv(openAIImagesReasoningEffortEnv, "")
	t.Setenv(openAIImagesPromptScaffoldEnv, "")
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL", "")
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL_FALLBACKS", "")

	body := []byte(`{"model":"gpt-image-2","prompt":"draw a red square","size":"1024x1024","output_format":"png","input_fidelity":"low"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	upstream := &httpUpstreamRecorder{resp: openAIImagesResponsesSSEFixture()}
	svc := newOpenAIImagesTestService(upstream)
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "medium", *result.ReasoningEffort)
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools.0.input_fidelity").Exists())
	require.Equal(t, "medium", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	require.Equal(t, openAIImagesResponsesMainModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String(), "Primary request:")
}

func TestForwardOpenAIImagesOAuth_QuotedLabelKeepsMediumAndVerbatimScaffold(t *testing.T) {
	t.Setenv(openAIImagesReasoningEffortEnv, "")
	t.Setenv(openAIImagesPromptScaffoldEnv, "")
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL", "")

	body := []byte(`{"model":"gpt-image-2","prompt":"cup text \"NOVA\"","size":"1024x1024","output_format":"png"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	upstream := &httpUpstreamRecorder{resp: openAIImagesResponsesSSEFixture()}
	svc := newOpenAIImagesTestService(upstream)
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "medium", *result.ReasoningEffort)
	require.Equal(t, "medium", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String(), "Text (verbatim):")
}

func TestForwardOpenAIImagesOAuth_EnvEffortAndScaffoldOffAreWhatGetsSent(t *testing.T) {
	t.Setenv(openAIImagesReasoningEffortEnv, "xhigh")
	t.Setenv(openAIImagesPromptScaffoldEnv, "off")
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL", "gpt-5.6-sol")
	t.Setenv("SUB2API_IMAGES_MAIN_MODEL_FALLBACKS", "")

	body := []byte(`{"model":"gpt-image-2","prompt":"draw a red square","size":"1024x1024","output_format":"png","input_fidelity":"high"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	upstream := &httpUpstreamRecorder{resp: openAIImagesResponsesSSEFixture()}
	svc := newOpenAIImagesTestService(upstream)
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
	require.NoError(t, err)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "xhigh", *result.ReasoningEffort)
	require.Equal(t, "xhigh", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools.0.input_fidelity").Exists())
	text := gjson.GetBytes(upstream.lastBody, "input.0.content.0.text").String()
	require.NotContains(t, text, "Primary request:")
	require.Contains(t, text, "draw a red square")
}

func TestForwardOpenAIImagesAPIKey_KeepsInputFidelityAndLeavesEffortUnset(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","input_fidelity":"high","response_format":"b64_json"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	recorder := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
	svc := newOpenAIImagesTestService(recorder)
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	result, err := svc.ForwardImages(context.Background(), c, newOpenAIImagesAPIKeyAccount(), body, parsed, "")
	require.NoError(t, err)
	require.Nil(t, result.ReasoningEffort)
	require.Equal(t, "high", gjson.GetBytes(recorder.lastBody, "input_fidelity").String())
	require.False(t, strings.Contains(string(recorder.lastBody), "reasoning"))
}

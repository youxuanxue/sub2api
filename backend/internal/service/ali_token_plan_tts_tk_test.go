//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildAliTokenPlanSpeechSynthesizerRequest_Positive(t *testing.T) {
	payload, model, text, err := buildAliTokenPlanSpeechSynthesizerRequest([]byte(`{
		"model":"qwen-audio-3.0-tts-plus",
		"input":"你好世界",
		"voice":"longanlingxin",
		"response_format":"mp3"
	}`))
	require.NoError(t, err)
	require.Equal(t, "qwen-audio-3.0-tts-plus", model)
	require.Equal(t, "你好世界", text)
	require.Contains(t, string(payload), `"text":"你好世界"`)
	require.Contains(t, string(payload), `"voice":"longanlingxin"`)
	require.Contains(t, string(payload), `"format":"mp3"`)
}

func TestBuildAliTokenPlanSpeechSynthesizerRequest_MissingInput(t *testing.T) {
	_, _, _, err := buildAliTokenPlanSpeechSynthesizerRequest([]byte(`{"model":"qwen-audio-3.0-tts-plus"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "input")
}

func TestBuildAliTokenPlanSpeechSynthesizerRequest_MissingModel(t *testing.T) {
	_, _, _, err := buildAliTokenPlanSpeechSynthesizerRequest([]byte(`{"input":"hi"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "model")
}

func TestBuildAliTokenPlanSpeechSynthesizerRequest_DefaultVoice(t *testing.T) {
	payload, _, _, err := buildAliTokenPlanSpeechSynthesizerRequest([]byte(`{
		"model":"qwen-audio-3.0-tts-plus",
		"input":"hello"
	}`))
	require.NoError(t, err)
	require.Contains(t, string(payload), `"voice":"longanlingxin"`)
}

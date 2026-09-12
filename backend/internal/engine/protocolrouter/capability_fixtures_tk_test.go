package protocolrouter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Deployment fixtures must enter the same parser as real requests. This is a
// parser integration check, not evidence of a successful upstream/gateway call.
func TestCapabilityFixturesPreserveRequestSemantics(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "ops", "stage0")
	raw, err := os.ReadFile(filepath.Join(root, "gateway-capability-matrix.json"))
	require.NoError(t, err)
	var manifest struct {
		Profiles []struct {
			ID          string `json:"id"`
			Protocol    string `json:"protocol"`
			RequestType string `json:"request_type"`
			Fixture     string `json:"fixture"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal(raw, &manifest))
	protocols := map[string]Protocol{"openai-chat": ProtocolChatCompletions, "openai-responses": ProtocolResponses,
		"anthropic-messages": ProtocolMessages, "gemini-content": ProtocolGeminiGenerateContent}
	for _, profile := range manifest.Profiles {
		inbound, ok := protocols[profile.Protocol]
		if !ok {
			continue
		} // Media endpoints have separate executors, not this parser.
		t.Run(profile.ID, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, profile.Fixture))
			require.NoError(t, err)
			var fixture struct {
				Body   json.RawMessage `json:"body"`
				Stream bool            `json:"stream"`
			}
			require.NoError(t, json.Unmarshal(data, &fixture))
			request, err := ParseCanonicalRequest(inbound, ResponsesPathRoot, "fixture-model", fixture.Stream, fixture.Body)
			require.NoError(t, err)
			require.JSONEq(t, string(fixture.Body), string(request.Body()))
			require.Equal(t, fixture.Stream, request.Profile().Stream)
			if profile.RequestType == "tool" {
				require.True(t, request.Profile().Tools)
			}
			// Native Gemini passes generationConfig/inlineData through unchanged; its
			// handler owns those fields, unlike the OpenAI/Anthropic profile vocabulary.
			if inbound != ProtocolGeminiGenerateContent {
				if profile.RequestType == "thinking" {
					require.Equal(t, ReasoningEffort, request.Profile().Reasoning)
				}
				if profile.RequestType == "multimodal" {
					require.NotZero(t, request.Profile().ContentKinds&ContentImage)
				}
			}
		})
	}
}

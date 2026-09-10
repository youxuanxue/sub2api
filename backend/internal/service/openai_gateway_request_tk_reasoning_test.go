package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCompatibleResponsesPreservesExplicitNoneForReasoningModels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  string
		target string
		known  bool
		want   string
	}{
		{name: "GPT through relay", model: "gpt-5.4", want: "none"},
		{name: "mapped GPT alias", model: "coding", target: "gpt-5.4", want: "none"},
		{name: "DeepSeek without metadata", model: "deepseek-v4-flash", want: "none"},
		{name: "mapped DeepSeek alias", model: "coding", target: "deepseek-v4-pro", want: "none"},
		{name: "discovered reasoning model", model: "custom-reasoner", known: true, want: "none"},
		{name: "catalog placeholder", model: "company-coding-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"base_url": "https://compat.example/v1",
			}}
			if tc.target != "" {
				account.Credentials["model_mapping"] = map[string]any{tc.model: tc.target}
			}
			if tc.known {
				reasoning := true
				account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{Models: map[string]UpstreamModelMetadata{
					tc.model: {Reasoning: &reasoning, SupportedReasoningLevels: []string{"none", "low", "high"}},
				}})
			}
			body := []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":"hi","reasoning":{"effort":"none","summary":"auto"},"reasoning_effort":"none"}`, tc.model))
			for name, normalize := range map[string]func(*Account, []byte) ([]byte, error){
				"HTTP":           filterOpenAIResponsesNoneReasoningEffortForAccount,
				"WS HTTP bridge": prepareOpenAIWSHTTPBridgeBody,
			} {
				t.Run(name, func(t *testing.T) {
					got, err := normalize(account, body)
					require.NoError(t, err)
					require.Equal(t, tc.want, gjson.GetBytes(got, "reasoning.effort").String())
					require.Equal(t, tc.want, gjson.GetBytes(got, "reasoning_effort").String())
					require.Equal(t, "auto", gjson.GetBytes(got, "reasoning.summary").String())
				})
			}
		})
	}
}

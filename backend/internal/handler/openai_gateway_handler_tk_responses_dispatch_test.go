//go:build unit

package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/tidwall/gjson"
)

// Test_tkApplyResponsesDispatchModelMapping pins the /v1/responses inbound model
// mapping that mirrors /v1/messages + /v1/chat/completions: a claude family model
// name must be rewritten to the group's configured gpt model before forwarding,
// so the Codex/ChatGPT backend does not reject the raw claude name with a 400.
func Test_tkApplyResponsesDispatchModelMapping(t *testing.T) {
	replace := func(body []byte, newModel string) []byte {
		return service.ReplaceModelInBody(body, newModel)
	}
	keyWith := func(cfg service.OpenAIMessagesDispatchModelConfig) *service.APIKey {
		return &service.APIKey{Group: &service.Group{
			ID:                          2,
			Platform:                    service.PlatformOpenAI,
			MessagesDispatchModelConfig: cfg,
		}}
	}
	bodyWithModel := func(model string) []byte {
		return []byte(`{"model":"` + model + `","input":[]}`)
	}

	cases := []struct {
		name      string
		apiKey    *service.APIKey
		body      []byte
		wantModel string
	}{
		{
			name:      "claude opus mapped to configured gpt model",
			apiKey:    keyWith(service.OpenAIMessagesDispatchModelConfig{OpusMappedModel: "gpt-5.5"}),
			body:      bodyWithModel("claude-opus-4-7"),
			wantModel: "gpt-5.5",
		},
		{
			name:      "claude opus uses default mapping when group config empty",
			apiKey:    keyWith(service.OpenAIMessagesDispatchModelConfig{}),
			body:      bodyWithModel("claude-opus-4-7"),
			wantModel: "gpt-5.5", // defaultOpenAIMessagesDispatchOpusMappedModel
		},
		{
			name:      "claude sonnet honours configured sonnet mapping",
			apiKey:    keyWith(service.OpenAIMessagesDispatchModelConfig{SonnetMappedModel: "gpt-5.5"}),
			body:      bodyWithModel("claude-sonnet-4-6"),
			wantModel: "gpt-5.5",
		},
		{
			name:      "non-claude model is left untouched",
			apiKey:    keyWith(service.OpenAIMessagesDispatchModelConfig{OpusMappedModel: "gpt-5.5"}),
			body:      bodyWithModel("gpt-5.5"),
			wantModel: "gpt-5.5",
		},
		{
			name:      "nil apiKey is a no-op",
			apiKey:    nil,
			body:      bodyWithModel("claude-opus-4-7"),
			wantModel: "claude-opus-4-7",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tkApplyResponsesDispatchModelMapping(tc.apiKey, tc.body, replace)
			got := gjson.GetBytes(out, "model").String()
			if got != tc.wantModel {
				t.Fatalf("model = %q, want %q", got, tc.wantModel)
			}
		})
	}

	// nil replace must never panic and must return the body verbatim.
	t.Run("nil replace is a no-op", func(t *testing.T) {
		body := bodyWithModel("claude-opus-4-7")
		out := tkApplyResponsesDispatchModelMapping(keyWith(service.OpenAIMessagesDispatchModelConfig{OpusMappedModel: "gpt-5.5"}), body, nil)
		if got := gjson.GetBytes(out, "model").String(); got != "claude-opus-4-7" {
			t.Fatalf("model = %q, want claude-opus-4-7", got)
		}
	})
}

// Test_tkResolveResponsesSelectionModel pins that account selection routes on the
// dispatch-mapped gpt model for claude family names (parity with /v1/messages) and
// leaves non-claude names / nil apiKey unchanged.
func Test_tkResolveResponsesSelectionModel(t *testing.T) {
	keyWith := func(cfg service.OpenAIMessagesDispatchModelConfig) *service.APIKey {
		return &service.APIKey{Group: &service.Group{ID: 2, Platform: service.PlatformOpenAI, MessagesDispatchModelConfig: cfg}}
	}

	cases := []struct {
		name      string
		apiKey    *service.APIKey
		requested string
		want      string
	}{
		{"claude opus routes on configured gpt", keyWith(service.OpenAIMessagesDispatchModelConfig{OpusMappedModel: "gpt-5.5"}), "claude-opus-4-7", "gpt-5.5"},
		{"claude opus routes on default gpt when unset", keyWith(service.OpenAIMessagesDispatchModelConfig{}), "claude-opus-4-7", "gpt-5.5"},
		{"non-claude model routes unchanged", keyWith(service.OpenAIMessagesDispatchModelConfig{OpusMappedModel: "gpt-5.5"}), "gpt-5.5", "gpt-5.5"},
		{"nil apiKey routes unchanged", nil, "claude-opus-4-7", "claude-opus-4-7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tkResolveResponsesSelectionModel(tc.apiKey, tc.requested); got != tc.want {
				t.Fatalf("selection model = %q, want %q", got, tc.want)
			}
		})
	}
}

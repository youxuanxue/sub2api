package service

import "testing"

func TestResolveOpenAIForwardModel(t *testing.T) {
	tests := []struct {
		name                        string
		account                     *Account
		requestedModel              string
		messagesDispatchMappedModel string
		expectedModel               string
	}{
		{
			name: "uses messages dispatch model for known claude family",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:              "claude-opus-4-6",
			messagesDispatchMappedModel: "gpt-4o-mini",
			expectedModel:               "gpt-4o-mini",
		},
		{
			name: "uses exact messages dispatch model for unknown claude family",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:              "claude-fable-5",
			messagesDispatchMappedModel: " gpt-5.6-sol ",
			expectedModel:               "gpt-5.6-sol",
		},
		{
			name:                        "nil account uses messages dispatch model",
			requestedModel:              "claude-fable-5",
			messagesDispatchMappedModel: "gpt-5.6-sol",
			expectedModel:               "gpt-5.6-sol",
		},
		{
			name:           "nil account without messages dispatch keeps requested model",
			requestedModel: "claude-fable-5",
			expectedModel:  "claude-fable-5",
		},
		{
			name: "ordinary unknown gpt model has no messages dispatch fallback",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel: "gpt6",
			expectedModel:  "gpt6",
		},
		{
			name: "account exact mapping overrides messages dispatch model",
			account: &Account{
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"claude-fable-5": "gpt-5.5",
					},
				},
			},
			requestedModel:              "claude-fable-5",
			messagesDispatchMappedModel: "gpt-5.6-sol",
			expectedModel:               "gpt-5.5",
		},
		{
			name: "account wildcard mapping overrides messages dispatch model",
			account: &Account{
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"claude-*": "gpt-5.4",
					},
				},
			},
			requestedModel:              "claude-fable-5",
			messagesDispatchMappedModel: "gpt-5.6-sol",
			expectedModel:               "gpt-5.4",
		},
		{
			name: "account passthrough mapping overrides messages dispatch model",
			account: &Account{
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"claude-fable-5": "claude-fable-5",
					},
				},
			},
			requestedModel:              "claude-fable-5",
			messagesDispatchMappedModel: "gpt-5.6-sol",
			expectedModel:               "claude-fable-5",
		},
		{
			name: "ordinary codex spark request keeps requested model",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel: "gpt-5.3-codex-spark",
			expectedModel:  "gpt-5.3-codex-spark",
		},
		{
			name: "ordinary gpt-5.5 request keeps requested model",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel: "gpt-5.5",
			expectedModel:  "gpt-5.5",
		},
		{
			name: "preserves gpt-5.5-pro before upstream normalization",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel: "gpt-5.5-pro",
			expectedModel:  "gpt-5.5-pro",
		},
		{
			name: "ordinary compact-spelled gpt5.5 request keeps requested model",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel: "gpt5.5",
			expectedModel:  "gpt5.5",
		},
		{
			name: "ordinary namespaced gpt-5.5 request keeps requested model",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel: "openai/gpt-5.5",
			expectedModel:  "openai/gpt-5.5",
		},
		{
			name: "ordinary compact gpt-5.5 request keeps requested model",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel: "gpt-5.5-openai-compact",
			expectedModel:  "gpt-5.5-openai-compact",
		},
		{
			name: "whitespace-only messages dispatch model is ignored",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:              "gpt-5.5",
			messagesDispatchMappedModel: "  ",
			expectedModel:               "gpt-5.5",
		},
		{
			name: "grok keeps native sku when default mapped model is set",
			account: &Account{
				Platform:    PlatformGrok,
				Credentials: map[string]any{},
			},
			requestedModel:     "grok-4.20-0309-reasoning",
			defaultMappedModel: "grok-code-fast-1",
			expectedModel:      "grok-4.20-0309-reasoning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveOpenAIForwardModel(tt.account, tt.requestedModel, tt.messagesDispatchMappedModel); got != tt.expectedModel {
				t.Fatalf("resolveOpenAIForwardModel(...) = %q, want %q", got, tt.expectedModel)
			}
		})
	}
}

func TestResolveOpenAICompactForwardModel(t *testing.T) {
	tests := []struct {
		name          string
		account       *Account
		model         string
		expectedModel string
	}{
		{
			name:          "nil account keeps original model",
			account:       nil,
			model:         "gpt-5.4",
			expectedModel: "gpt-5.4",
		},
		{
			name: "missing compact mapping keeps original model",
			account: &Account{
				Credentials: map[string]any{},
			},
			model:         "gpt-5.4",
			expectedModel: "gpt-5.4",
		},
		{
			name: "exact compact mapping overrides model",
			account: &Account{
				Credentials: map[string]any{
					"compact_model_mapping": map[string]any{
						"gpt-5.4": "gpt-5.4-openai-compact",
					},
				},
			},
			model:         "gpt-5.4",
			expectedModel: "gpt-5.4-openai-compact",
		},
		{
			name: "wildcard compact mapping overrides model",
			account: &Account{
				Credentials: map[string]any{
					"compact_model_mapping": map[string]any{
						"gpt-5.*": "gpt-5-openai-compact",
					},
				},
			},
			model:         "gpt-5.4",
			expectedModel: "gpt-5-openai-compact",
		},
		{
			name: "passthrough compact mapping remains unchanged",
			account: &Account{
				Credentials: map[string]any{
					"compact_model_mapping": map[string]any{
						"gpt-5.4": "gpt-5.4",
					},
				},
			},
			model:         "gpt-5.4",
			expectedModel: "gpt-5.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveOpenAICompactForwardModel(tt.account, tt.model); got != tt.expectedModel {
				t.Fatalf("resolveOpenAICompactForwardModel(...) = %q, want %q", got, tt.expectedModel)
			}
		})
	}
}

// TestNormalizeCodexModel pins the algorithm branches (version-prefix suffix
// stripping, image-generation passthrough, unknown-model passthrough) — NOT
// the codexModelMap alias table itself, which TestNormalizeOpenAIModelForUpstream
// already exercises through the OAuth path for the specific aliases that
// matter (gpt-5.3, codex-mini-latest, ...). Duplicating those literal map
// entries here would just mirror the SSOT table instead of testing behavior.
func TestNormalizeCodexModel(t *testing.T) {
	cases := map[string]string{
		"gpt-5.3-codex-spark":       "gpt-5.3-codex-spark", // exact prefix match
		"gpt-5.3-codex-spark-high":  "gpt-5.3-codex-spark", // suffix stripped via codexVersionModelPrefixes
		"gpt-5.3-codex-spark-xhigh": "gpt-5.3-codex-spark",
		"gpt-5.3-codex":             "gpt-5.3-codex-spark", // non-display legacy alias
		"gpt-5.3-codex-high":        "gpt-5.3-codex-spark",
		"gpt-5-codex-xhigh":         "gpt-5.3-codex-spark",
		"gpt-image-2":               "gpt-image-2",     // image-generation models pass through unmapped
		"gpt-5.4-nano-high":         "gpt-5.4-nano",    // unknown reasoning-effort suffix stripped
		"gpt6":                      "gpt6",            // unknown gpt model passes through unchanged
		"claude-opus-4-6":           "claude-opus-4-6", // non-gpt model passes through unchanged
	}

	for input, expected := range cases {
		if got := normalizeCodexModel(input); got != expected {
			t.Fatalf("normalizeCodexModel(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestNormalizeOpenAIModelForUpstream(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		model   string
		want    string
	}{
		{
			name:    "oauth routes bare GPT-5.6 alias to Sol",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5.6",
			want:    "gpt-5.6-sol",
		},
		{
			name:    "oauth routes provider-prefixed GPT-5.6 alias to Sol",
			account: &Account{Type: AccountTypeOAuth},
			model:   "openai/gpt-5.6",
			want:    "gpt-5.6-sol",
		},
		{
			name:    "oauth preserves unknown non codex model",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gemini-3-flash-preview",
			want:    "gemini-3-flash-preview",
		},
		{
			name:    "oauth preserves invalid gpt model",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt6",
			want:    "gpt6",
		},
		{
			name:    "oauth normalizes known codex alias",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5.4-high",
			want:    "gpt-5.4",
		},
		{
			name:    "oauth routes GPT-5.5 Pro alias to GPT-5.5",
			account: &Account{Type: AccountTypeOAuth},
			model:   "openai/gpt-5.5-pro",
			want:    "gpt-5.5",
		},
		{
			name:    "oauth preserves codex auto review model",
			account: &Account{Type: AccountTypeOAuth},
			model:   "codex-auto-review",
			want:    "codex-auto-review",
		},
		{
			name:    "oauth maps legacy gpt-5.3-codex alias to spark",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5.3-codex",
			want:    "gpt-5.3-codex-spark",
		},
		{
			name:    "oauth maps bare gpt-5.3 alias to spark",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5.3",
			want:    "gpt-5.3-codex-spark",
		},
		{
			name:    "oauth normalizes codex-mini-latest alias to spark via codexModelMap",
			account: &Account{Type: AccountTypeOAuth},
			model:   "codex-mini-latest",
			want:    "gpt-5.3-codex-spark",
		},
		{
			name:    "oauth routes gpt-5.3-chat-latest alias to spark",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5.3-chat-latest",
			want:    "gpt-5.3-codex-spark",
		},
		{
			name:    "oauth spark model not remapped",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5.3-codex-spark",
			want:    "gpt-5.3-codex-spark",
		},
		{
			name:    "apikey preserves custom compatible model",
			account: &Account{Type: AccountTypeAPIKey},
			model:   "gemini-3-flash-preview",
			want:    "gemini-3-flash-preview",
		},
		{
			name:    "apikey preserves official non codex model",
			account: &Account{Type: AccountTypeAPIKey},
			model:   "gpt-4.1",
			want:    "gpt-4.1",
		},
		{
			name: "grok oauth keeps native sku unchanged",
			account: &Account{
				Platform: PlatformGrok,
				Type:     AccountTypeOAuth,
			},
			model: "grok-build-0.1",
			want:  "grok-build-0.1",
		},
		{
			name: "grok apikey keeps native sku unchanged",
			account: &Account{
				Platform: PlatformGrok,
				Type:     AccountTypeAPIKey,
			},
			model: "grok-code-fast-1",
			want:  "grok-code-fast-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeOpenAIModelForUpstream(tt.account, tt.model); got != tt.want {
				t.Fatalf("normalizeOpenAIModelForUpstream(...) = %q, want %q", got, tt.want)
			}
		})
	}
}

// The ChatGPT OAuth rewrite table must stay empty until the upstream contract
// flips again (2026-06-10 prod probe): canonical names go upstream as-is so
// clients see the real upstream 400 instead of a silently rewritten model.
func TestChatGPTOAuthUpstreamModelNamesIsEmpty(t *testing.T) {
	if len(chatGPTOAuthUpstreamModelNames) != 0 {
		t.Fatalf("chatGPTOAuthUpstreamModelNames = %#v, want empty map (see contract timeline in openai_codex_transform.go)", chatGPTOAuthUpstreamModelNames)
	}
}

func TestUsageBillingModelCandidatesPreserveCodexAutoReviewModel(t *testing.T) {
	candidates := usageBillingModelCandidates("codex-auto-review")

	expected := []string{"codex-auto-review"}
	if len(candidates) != len(expected) {
		t.Fatalf("usageBillingModelCandidates(codex-auto-review) = %#v, want %#v", candidates, expected)
	}
	for i := range expected {
		if candidates[i] != expected[i] {
			t.Fatalf("usageBillingModelCandidates(codex-auto-review) = %#v, want %#v", candidates, expected)
		}
	}
}

func TestUsageBillingModelCandidatesRouteGPT55ProAlias(t *testing.T) {
	candidates := usageBillingModelCandidates("openai/gpt-5.5-pro")

	expected := []string{"openai/gpt-5.5-pro", "gpt-5.5-pro", "gpt-5.5"}
	if len(candidates) != len(expected) {
		t.Fatalf("usageBillingModelCandidates(openai/gpt-5.5-pro) = %#v, want %#v", candidates, expected)
	}
	for i := range expected {
		if candidates[i] != expected[i] {
			t.Fatalf("usageBillingModelCandidates(openai/gpt-5.5-pro) = %#v, want %#v", candidates, expected)
		}
	}
}

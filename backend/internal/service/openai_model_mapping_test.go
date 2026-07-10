package service

import "testing"

func TestResolveOpenAIForwardModel(t *testing.T) {
	tests := []struct {
		name               string
		account            *Account
		requestedModel     string
		defaultMappedModel string
		expectedModel      string
	}{
		{
			name: "uses messages dispatch default for claude model",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:     "claude-opus-4-6",
			defaultMappedModel: "gpt-4o-mini",
			expectedModel:      "gpt-4o-mini",
		},
		{
			name: "does not fall back to group default for invalid gpt model",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:     "gpt6",
			defaultMappedModel: "gpt-5.4",
			expectedModel:      "gpt6",
		},
		{
			name: "preserves explicit gpt-5.4 instead of group default",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:     "gpt-5.4",
			defaultMappedModel: "gpt-4o-mini",
			expectedModel:      "gpt-5.4",
		},
		{
			name: "preserves exact passthrough mapping instead of group default",
			account: &Account{
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"gpt-5.4": "gpt-5.4",
					},
				},
			},
			requestedModel:     "gpt-5.4",
			defaultMappedModel: "gpt-4o-mini",
			expectedModel:      "gpt-5.4",
		},
		{
			name: "preserves wildcard passthrough mapping instead of group default",
			account: &Account{
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"gpt-*": "gpt-5.4",
					},
				},
			},
			requestedModel:     "gpt-5.4",
			defaultMappedModel: "gpt-4o-mini",
			expectedModel:      "gpt-5.4",
		},
		{
			name: "uses account remap when explicit target differs",
			account: &Account{
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"gpt-5": "gpt-5.4",
					},
				},
			},
			requestedModel:     "gpt-5",
			defaultMappedModel: "gpt-4o-mini",
			expectedModel:      "gpt-5.4",
		},
		{
			name: "preserves codex spark instead of group default",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:     "gpt-5.3-codex-spark",
			defaultMappedModel: "gpt-5.4",
			expectedModel:      "gpt-5.3-codex-spark",
		},
		{
			name: "preserves gpt-5.5 instead of group default",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:     "gpt-5.5",
			defaultMappedModel: "gpt-5.4",
			expectedModel:      "gpt-5.5",
		},
		{
			name: "preserves gpt-5.5-pro instead of group default",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:     "gpt-5.5-pro",
			defaultMappedModel: "gpt-5.5",
			expectedModel:      "gpt-5.5-pro",
		},
		{
			name: "preserves compact-spelled gpt5.5 instead of group default",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:     "gpt5.5",
			defaultMappedModel: "gpt-5.4",
			expectedModel:      "gpt5.5",
		},
		{
			name: "preserves openai namespaced gpt-5.5 instead of group default",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:     "openai/gpt-5.5",
			defaultMappedModel: "gpt-5.4",
			expectedModel:      "openai/gpt-5.5",
		},
		{
			name: "preserves compact gpt-5.5 instead of group default",
			account: &Account{
				Credentials: map[string]any{},
			},
			requestedModel:     "gpt-5.5-openai-compact",
			defaultMappedModel: "gpt-5.4",
			expectedModel:      "gpt-5.5-openai-compact",
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
			if got := resolveOpenAIForwardModel(tt.account, tt.requestedModel, tt.defaultMappedModel); got != tt.expectedModel {
				t.Fatalf("resolveOpenAIForwardModel(...) = %q, want %q", got, tt.expectedModel)
			}
		})
	}
}

func TestResolveOpenAIForwardModel_PreventsClaudeModelFromFallingBackToGpt54(t *testing.T) {
	account := &Account{
		Credentials: map[string]any{},
	}

	withoutDefault := resolveOpenAIForwardModel(account, "claude-opus-4-6", "")
	if withoutDefault != "claude-opus-4-6" {
		t.Fatalf("resolveOpenAIForwardModel(...) = %q, want %q", withoutDefault, "claude-opus-4-6")
	}

	withDefault := resolveOpenAIForwardModel(account, "claude-opus-4-6", "gpt-5.4")
	if withDefault != "gpt-5.4" {
		t.Fatalf("resolveOpenAIForwardModel(...) = %q, want %q", withDefault, "gpt-5.4")
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
			name:    "oauth preserves GPT-5.5 Pro model",
			account: &Account{Type: AccountTypeOAuth},
			model:   "openai/gpt-5.5-pro",
			want:    "gpt-5.5-pro",
		},
		{
			name:    "oauth preserves codex auto review model",
			account: &Account{Type: AccountTypeOAuth},
			model:   "codex-auto-review",
			want:    "codex-auto-review",
		},
		{
			name:    "oauth preserves legacy gpt-5.3-codex for deprecated gate",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5.3-codex",
			want:    "gpt-5.3-codex",
		},
		{
			name:    "oauth maps bare gpt-5.3 alias to official chat id",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5.3",
			want:    "gpt-5.3-chat-latest",
		},
		{
			name:    "oauth normalizes codex-mini-latest alias to spark via codexModelMap",
			account: &Account{Type: AccountTypeOAuth},
			model:   "codex-mini-latest",
			want:    "gpt-5.3-codex-spark",
		},
		{
			name:    "oauth keeps gpt-5.3-chat-latest unrewritten (SSOT audit hotfix: substring fallback would otherwise misclassify it as gpt-5.3-codex)",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5.3-chat-latest",
			want:    "gpt-5.3-chat-latest",
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

func TestUsageBillingModelCandidatesPreserveGPT55ProModel(t *testing.T) {
	candidates := usageBillingModelCandidates("openai/gpt-5.5-pro")

	expected := []string{"openai/gpt-5.5-pro", "gpt-5.5-pro"}
	if len(candidates) != len(expected) {
		t.Fatalf("usageBillingModelCandidates(openai/gpt-5.5-pro) = %#v, want %#v", candidates, expected)
	}
	for i := range expected {
		if candidates[i] != expected[i] {
			t.Fatalf("usageBillingModelCandidates(openai/gpt-5.5-pro) = %#v, want %#v", candidates, expected)
		}
	}
}

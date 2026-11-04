package service

import "testing"

func TestTkResolveOpenAIBareModelAlias(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		wantID   string
		wantBool bool
	}{
		{"bare gpt-5.3 resolves to chat-latest", "gpt-5.3", "gpt-5.3-chat-latest", true},
		{"already-resolved id passes through unmatched", "gpt-5.3-chat-latest", "", false},
		{"unrelated current model passes through unmatched", "gpt-5.4", "", false},
		{"empty string passes through unmatched", "", "", false},
		{"non-openai model passes through unmatched", "claude-sonnet-4-6", "", false},
		{"spelling variant is not normalized (exact-match only)", "gpt5.3", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := TkResolveOpenAIBareModelAlias(tc.model)
			if got != tc.wantID || ok != tc.wantBool {
				t.Fatalf("TkResolveOpenAIBareModelAlias(%q) = (%q, %v), want (%q, %v)",
					tc.model, got, ok, tc.wantID, tc.wantBool)
			}
		})
	}
}

// Guards against silent table edits: the bare-alias table is a deliberately
// tiny, hand-curated set of same-lineage renames (never a family-derived
// mapping). Adding/removing an entry should be an intentional, reviewed change.
func TestTkOpenAIBareModelAliasesTableIsExhaustive(t *testing.T) {
	expected := map[string]string{
		"gpt-5.3": "gpt-5.3-chat-latest",
	}
	if len(tkOpenAIBareModelAliases) != len(expected) {
		t.Fatalf("bare-alias table size changed (got %d, want %d); update this assertion intentionally",
			len(tkOpenAIBareModelAliases), len(expected))
	}
	for model, want := range expected {
		got, ok := tkOpenAIBareModelAliases[model]
		if !ok || got != want {
			t.Fatalf("bare-alias table entry %q = (%q, %v), want %q", model, got, ok, want)
		}
	}
}

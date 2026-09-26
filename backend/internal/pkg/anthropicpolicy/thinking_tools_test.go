package anthropicpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeThinkingTools(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		capabilities Capabilities
		changed      bool
	}{
		{"adaptive forced", `{"thinking":{"type":"adaptive"},"tool_choice":{"type":"tool","name":"lookup","disable_parallel_tool_use":true}}`, Capabilities{}, true},
		{"optional off", `{"thinking":{"type":"disabled"},"tool_choice":{"type":"any"}}`, Capabilities{}, false},
		{"implicit always on", `{"tool_choice":{"type":"tool","name":"lookup"}}`, Capabilities{AlwaysThinking: true}, true},
		{"no invented thinking", `{"tool_choice":{"type":"any"}}`, Capabilities{}, false},
		{"none", `{"thinking":{"type":"adaptive"},"tool_choice":{"type":"none"}}`, Capabilities{AlwaysThinking: true}, false},
		{"exact endpoint", `{"thinking":{"type":"adaptive"},"tool_choice":{"type":"any"}}`, Capabilities{ForcedToolsWithThinking: true}, false},
		{"invalid json", `{"thinking":{"type":"adaptive"},"tool_choice":{"type":"any"}`, Capabilities{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			out, changed := Normalize(body, true, tc.capabilities)
			require.Equal(t, tc.changed, changed)
			require.Equal(t, tc.body, string(body))
			if !changed {
				require.Equal(t, body, out)
				return
			}
			require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice.type").String())
			require.False(t, gjson.GetBytes(out, "tool_choice.name").Exists())
			require.Equal(t, gjson.GetBytes(body, "thinking").Raw, gjson.GetBytes(out, "thinking").Raw)
			require.Equal(t, gjson.GetBytes(body, "tool_choice.disable_parallel_tool_use").Raw, gjson.GetBytes(out, "tool_choice.disable_parallel_tool_use").Raw)
		})
	}
}

func TestNormalizeFableThinkingModes(t *testing.T) {
	for _, mode := range []string{`{"type":"enabled","budget_tokens":4096}`, `{"type":"disabled"}`} {
		body := []byte(`{"thinking":` + mode + `,"tool_choice":{"type":"any"}}`)
		out, changed := Normalize(body, true, MessagesCapabilities("claude-fable-5-1"))
		require.True(t, changed)
		require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice.type").String())
		require.False(t, gjson.GetBytes(out, "thinking.budget_tokens").Exists())
		if gjson.GetBytes(body, "thinking.type").String() == "disabled" {
			require.False(t, gjson.GetBytes(out, "thinking").Exists())
		} else {
			require.Equal(t, "adaptive", gjson.GetBytes(out, "thinking.type").String())
		}
	}
}

// NormalizeValidated skips the JSON scan its caller already performed. It must
// therefore agree with Normalize on every well-formed body: a divergence would
// make the per-route fast path apply a different thinking/tool policy than the
// audited one.
func TestNormalizeValidatedMatchesNormalizeOnValidBodies(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		capabilities Capabilities
	}{
		{"adaptive forced", `{"thinking":{"type":"adaptive"},"tool_choice":{"type":"tool","name":"lookup","disable_parallel_tool_use":true}}`, Capabilities{}},
		{"optional off", `{"thinking":{"type":"disabled"},"tool_choice":{"type":"any"}}`, Capabilities{}},
		{"implicit always on", `{"tool_choice":{"type":"tool","name":"lookup"}}`, Capabilities{AlwaysThinking: true}},
		{"adaptive only", `{"thinking":{"type":"enabled","budget_tokens":2048},"tool_choice":{"type":"any"}}`, Capabilities{AdaptiveOnlyThinking: true}},
		{"none stays", `{"thinking":{"type":"adaptive"},"tool_choice":{"type":"none"}}`, Capabilities{AlwaysThinking: true}},
		{"exact endpoint", `{"thinking":{"type":"adaptive"},"tool_choice":{"type":"any"}}`, Capabilities{ForcedToolsWithThinking: true}},
		{"reasoning effort chat", `{"reasoning_effort":"high","tool_choice":"required"}`, Capabilities{}},
	} {
		for _, messages := range []bool{true, false} {
			t.Run(tc.name, func(t *testing.T) {
				want, wantChanged := Normalize([]byte(tc.body), messages, tc.capabilities)
				got, gotChanged := NormalizeValidated([]byte(tc.body), messages, tc.capabilities)
				require.Equal(t, wantChanged, gotChanged)
				require.Equal(t, string(want), string(got))
			})
		}
	}
}

// Facts is the body-derived half of the decision, so deriving it once up front
// and deciding later must match reading the body inside the decision. This is
// what lets a caller hold one Facts across many per-route capability sets.
func TestNormalizeWithFactsMatchesNormalizeAcrossCapabilities(t *testing.T) {
	bodies := []string{
		`{"thinking":{"type":"adaptive"},"tool_choice":{"type":"tool","name":"lookup","disable_parallel_tool_use":true}}`,
		`{"thinking":{"type":"adaptive"},"tool_choice":{"type":"tool","name":"lookup","disable_parallel_tool_use":false}}`,
		`{"thinking":{"type":"adaptive"},"tool_choice":{"type":"any"}}`,
		`{"thinking":{"type":"disabled"},"tool_choice":"required"}`,
		`{"thinking":{"type":"enabled","budget_tokens":2048},"tool_choice":{"type":"function"}}`,
		`{"reasoning":{"effort":"medium"},"tool_choice":"required"}`,
		`{"reasoning_effort":"xhigh","tool_choice":{"type":"function"}}`,
		`{"tool_choice":{"type":"none"}}`,
		`{"model":"claude-fable-5"}`,
	}
	capabilities := []Capabilities{
		{},
		{AlwaysThinking: true},
		{AdaptiveOnlyThinking: true},
		{AlwaysThinking: true, AdaptiveOnlyThinking: true},
		{ForcedToolsWithThinking: true},
		{AlwaysThinking: true, ForcedToolsWithThinking: true},
	}
	for _, body := range bodies {
		for _, messages := range []bool{true, false} {
			// One derivation, reused for every capability set, exactly as a caller
			// evaluating many candidate routes over one immutable body would.
			facts := InspectValidated([]byte(body), messages)
			for _, capability := range capabilities {
				want, wantChanged := Normalize([]byte(body), messages, capability)
				got, gotChanged := NormalizeWithFacts([]byte(body), messages, facts, capability)
				require.Equal(t, wantChanged, gotChanged, "body=%s messages=%v caps=%+v", body, messages, capability)
				require.Equal(t, string(want), string(got), "body=%s messages=%v caps=%+v", body, messages, capability)
			}
		}
	}
}

// Normalize keeps its own guard for callers that cannot prove validity.
func TestNormalizeRejectsMalformedBody(t *testing.T) {
	body := []byte(`{"thinking":{"type":"adaptive"},"tool_choice":{"type":"any"}`)
	out, changed := Normalize(body, true, Capabilities{})
	require.False(t, changed)
	require.Equal(t, body, out)
}

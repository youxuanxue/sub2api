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

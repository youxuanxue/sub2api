//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyKiroChatCompletionsReasoningEffort_PassthroughXHighAndMax(t *testing.T) {
	base := []byte(`{"model":"claude-opus-5","messages":[]}`)
	for _, effort := range []string{"xhigh", "max", "high", "medium", "low"} {
		got := applyKiroChatCompletionsReasoningEffort(
			[]byte(`{"reasoning_effort":"`+effort+`"}`),
			append([]byte(nil), base...),
		)
		require.Equal(t, effort, gjson.GetBytes(got, "output_config.effort").String(), effort)
		require.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String(), effort)
	}

	nested := applyKiroChatCompletionsReasoningEffort(
		[]byte(`{"reasoning":{"effort":"xhigh"}}`),
		append([]byte(nil), base...),
	)
	require.Equal(t, "xhigh", gjson.GetBytes(nested, "output_config.effort").String())

	unchanged := applyKiroChatCompletionsReasoningEffort(
		[]byte(`{"reasoning_effort":"ultra"}`),
		append([]byte(nil), base...),
	)
	require.False(t, gjson.GetBytes(unchanged, "output_config.effort").Exists())
	require.JSONEq(t, string(base), string(unchanged))
}

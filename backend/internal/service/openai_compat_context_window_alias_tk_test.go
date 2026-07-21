//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestApplyOpenAICompatContextWindowModelAlias(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "gpt-5.5[1m]", want: "gpt-5.5", ok: true},
		{in: "gpt-5.5[1M]", want: "gpt-5.5", ok: true},
		{in: "gpt-5.4[200k]", want: "gpt-5.4", ok: true},
		{in: "gpt-5.5", want: "gpt-5.5", ok: false},
		{in: "claude-opus-4-8[1m]", want: "claude-opus-4-8", ok: true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, stripped := applyOpenAICompatContextWindowModelAlias(tc.in)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.ok, stripped)
		})
	}
}

func TestNormalizeOpenAICompatRequestedModel_StripsContextWindowAlias(t *testing.T) {
	t.Parallel()

	require.Equal(t, "gpt-5.5", NormalizeOpenAICompatRequestedModel("gpt-5.5[1m]"))
	require.Equal(t, "gpt-5.4", NormalizeOpenAICompatRequestedModel("gpt-5.4-xhigh[1m]"))
}

func TestApplyOpenAICompatModelNormalization_StripsContextWindowAliasBeforeReasoning(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{Model: "gpt-5.5-xhigh[1m]"}

	applyOpenAICompatModelNormalization(req)

	require.Equal(t, "gpt-5.5", req.Model)
	require.NotNil(t, req.OutputConfig)
	require.Equal(t, "max", req.OutputConfig.Effort)
}

func TestNormalizeOpenAIMessagesDispatchMappedModel_StripsContextWindowAlias(t *testing.T) {
	t.Parallel()

	require.Equal(t, "gpt-5.5", normalizeOpenAIMessagesDispatchMappedModel("gpt-5.5[1m]"))
}

func TestNormalizeCodexModel_StripsContextWindowAlias(t *testing.T) {
	t.Parallel()

	require.Equal(t, "gpt-5.5", normalizeCodexModel("gpt-5.5[1m]"))
}

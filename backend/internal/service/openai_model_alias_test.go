//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalizeOpenAICompatRoutingModel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"gpt5.4-mini", "gpt-5.4-mini"},
		{" GPT-5.4-Mini ", "gpt-5.4-mini"},
		{"gpt-5.4-mini", "gpt-5.4-mini"},
		{"qwen-max", "qwen-max"},
		{"", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, CanonicalizeOpenAICompatRoutingModel(tc.in))
		})
	}
}

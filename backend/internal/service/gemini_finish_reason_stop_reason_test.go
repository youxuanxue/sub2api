//go:build unit

package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMapGeminiFinishReasonToClaudeStopReason(t *testing.T) {
	tests := []struct {
		finishReason string
		want         string
	}{
		{finishReason: "SAFETY", want: "refusal"},
		{finishReason: "RECITATION", want: "refusal"},
		{finishReason: "LANGUAGE", want: "refusal"},
		{finishReason: "BLOCKLIST", want: "refusal"},
		{finishReason: "PROHIBITED_CONTENT", want: "refusal"},
		{finishReason: "SPII", want: "refusal"},
		{finishReason: "IMAGE_SAFETY", want: "refusal"},
		{finishReason: "IMAGE_PROHIBITED_CONTENT", want: "refusal"},
		{finishReason: "IMAGE_RECITATION", want: "refusal"},
		{finishReason: "safety", want: "refusal"},
		{finishReason: " safety ", want: "refusal"},
		{finishReason: "image_recitation", want: "refusal"},
		{finishReason: "  blocklist\t", want: "refusal"},
		{finishReason: "STOP", want: "end_turn"},
		{finishReason: "MAX_TOKENS", want: "max_tokens"},
		{finishReason: "", want: "end_turn"},
		{finishReason: "MALFORMED_FUNCTION_CALL", want: "end_turn"},
		{finishReason: "OTHER", want: "end_turn"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.finishReason), func(t *testing.T) {
			require.Equal(t, tt.want, mapGeminiFinishReasonToClaudeStopReason(tt.finishReason))
		})
	}
}

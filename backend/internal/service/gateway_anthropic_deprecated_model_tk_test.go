package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTkIsDeprecatedAnthropicModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name           string
		model          string
		wantDeprecated bool
		wantReplaceTo  string
	}{
		{"3-haiku retired -> sonnet", "claude-3-haiku-20240307", true, tkDeprecatedAnthropicReplacementSonnet},
		{"3-sonnet retired -> sonnet", "claude-3-sonnet-20240229", true, tkDeprecatedAnthropicReplacementSonnet},
		{"3-opus retired -> opus", "claude-3-opus-20240229", true, tkDeprecatedAnthropicReplacementOpus},
		{"3-5-haiku retired -> sonnet", "claude-3-5-haiku-20241022", true, tkDeprecatedAnthropicReplacementSonnet},
		{"3-5-sonnet old retired -> sonnet", "claude-3-5-sonnet-20240620", true, tkDeprecatedAnthropicReplacementSonnet},
		{"3-5-sonnet new retired -> sonnet", "claude-3-5-sonnet-20241022", true, tkDeprecatedAnthropicReplacementSonnet},
		{"3-7-sonnet retired -> sonnet", "claude-3-7-sonnet-20250219", true, tkDeprecatedAnthropicReplacementSonnet},
		{"4-0-sonnet sunsetting -> sonnet", "claude-sonnet-4-20250514", true, tkDeprecatedAnthropicReplacementSonnet},
		{"4-0-opus sunsetting -> opus", "claude-opus-4-20250514", true, tkDeprecatedAnthropicReplacementOpus},

		{"sonnet-4-6 current passes", "claude-sonnet-4-6", false, ""},
		{"opus-4-7 current passes", "claude-opus-4-7", false, ""},
		{"sonnet-4-5 current passes", "claude-sonnet-4-5", false, ""},
		{"empty string passes", "", false, ""},
		{"non-claude model passes", "gpt-5", false, ""},
		{"short-id without snapshot passes", "claude-3-5-sonnet", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replacement, deprecated := tkIsDeprecatedAnthropicModel(tc.model)
			require.Equal(t, tc.wantDeprecated, deprecated, "deprecated flag for %q", tc.model)
			if tc.wantDeprecated {
				require.Equal(t, tc.wantReplaceTo, replacement)
			} else {
				require.Empty(t, replacement)
			}
		})
	}
}

func TestTkBuildDeprecatedAnthropicMessage(t *testing.T) {
	msg := tkBuildDeprecatedAnthropicMessage("claude-3-5-sonnet-20241022", tkDeprecatedAnthropicReplacementSonnet)
	require.Contains(t, msg, "claude-3-5-sonnet-20241022", "must echo the requested model")
	require.Contains(t, msg, tkDeprecatedAnthropicReplacementSonnet, "must suggest sonnet replacement")
	require.Contains(t, msg, tkDeprecatedAnthropicReplacementOpus, "must mention opus replacement")
	require.Contains(t, msg, "anthropic.com", "must link to Anthropic docs")
}

func TestTkBuildDeprecatedAnthropicMessageEmptyReplacementFallsBackToSonnet(t *testing.T) {
	msg := tkBuildDeprecatedAnthropicMessage("claude-3-opus-20240229", "")
	require.Contains(t, msg, tkDeprecatedAnthropicReplacementSonnet,
		"empty replacement should fall back to the sonnet default so the customer message never reads 'migrate to '''")
}

func TestTkWriteAnthropicDeprecatedModelError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	wrote := tkWriteAnthropicDeprecatedModelError(c, "claude-3-5-sonnet-20241022", tkDeprecatedAnthropicReplacementSonnet)
	require.True(t, wrote)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.True(t, c.IsAborted(), "must abort further gin handlers")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload),
		"response body must be valid JSON in Anthropic shape")

	require.Equal(t, "error", payload["type"], "top-level type must be 'error' for Anthropic clients")

	errObj, ok := payload["error"].(map[string]any)
	require.True(t, ok, "error field must be an object, got %T", payload["error"])
	require.Equal(t, tkDeprecatedAnthropicErrorType, errObj["type"])

	message, _ := errObj["message"].(string)
	require.Contains(t, message, "claude-3-5-sonnet-20241022")
	require.Contains(t, message, tkDeprecatedAnthropicReplacementSonnet)
	require.True(t, strings.Contains(message, "retired") || strings.Contains(message, "sunset"),
		"message should explain why the model is rejected")
}

func TestTkWriteAnthropicDeprecatedModelErrorNilContextIsSafe(t *testing.T) {
	wrote := tkWriteAnthropicDeprecatedModelError(nil, "anything", "anything")
	require.False(t, wrote, "nil context must be a safe no-op")
}

// Guards against silent table edits during upstream merges. The set of
// retired IDs is the contract the customer-facing migration message refers
// to; adding/removing one is a deliberate change that should not slip in
// alongside an unrelated diff. Update this assertion explicitly when the
// list changes.
func TestTkDeprecatedAnthropicModelsTableIsExhaustive(t *testing.T) {
	expected := map[string]struct{}{
		"claude-3-haiku-20240307":    {},
		"claude-3-sonnet-20240229":   {},
		"claude-3-opus-20240229":     {},
		"claude-3-5-haiku-20241022":  {},
		"claude-3-5-sonnet-20240620": {},
		"claude-3-5-sonnet-20241022": {},
		"claude-3-7-sonnet-20250219": {},
		"claude-sonnet-4-20250514":   {},
		"claude-opus-4-20250514":     {},
	}
	require.Len(t, tkDeprecatedAnthropicModels, len(expected),
		"retired model table size changed; update this assertion intentionally")
	for id := range expected {
		_, ok := tkDeprecatedAnthropicModels[id]
		require.Truef(t, ok, "retired model %q must remain on the gate list", id)
	}
}

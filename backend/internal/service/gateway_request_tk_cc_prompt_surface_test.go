package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountGetOAuthAccountEmail(t *testing.T) {
	require.Equal(t, "", (*Account)(nil).GetOAuthAccountEmail())
	require.Equal(t, "", (&Account{Type: AccountTypeAPIKey}).GetOAuthAccountEmail())
	acct := &Account{
		Type:  AccountTypeOAuth,
		Extra: map[string]any{"email_address": " edge@tokenkey.dev "},
		Credentials: map[string]any{
			"email": "fallback@example.com",
		},
	}
	require.Equal(t, "edge@tokenkey.dev", acct.GetOAuthAccountEmail())
}

func TestTkStripCCEnvironmentSection(t *testing.T) {
	in := "<system-reminder>\n# Environment\nTZ=Asia/Shanghai\nProxy=http://127.0.0.1:7890\n\nThe user's email address is client@gmail.com.\n\n# currentDate\nToday's date is 2026/07/01.\n</system-reminder>"
	out, changed := tkStripCCEnvironmentSection(in)
	require.True(t, changed)
	require.NotContains(t, out, "# Environment")
	require.NotContains(t, out, "Asia/Shanghai")
	require.Contains(t, out, "client@gmail.com")
	require.Contains(t, out, "# currentDate")
}

func TestTkNormalizeCCUserEmailLine_ReplaceWithOAuth(t *testing.T) {
	in := "The user's email address is client@gmail.com."
	out, changed := tkNormalizeCCUserEmailLine(in, "edge-oauth@tokenkey.dev")
	require.True(t, changed)
	require.Contains(t, out, "edge-oauth@tokenkey.dev")
	require.NotContains(t, out, "client@gmail.com")
}

func TestTkNormalizeAnthropicCCPromptSurfaceMessagesEnvironmentAndEmail(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\n# Environment\nTZ=Asia/Shanghai\n\nThe user's email address is client@gmail.com.\n\n# currentDate\nToday\u2019s date is 2026/07/01.\n</system-reminder>"}]}]}`)
	out, changed := tkNormalizeAnthropicCCPromptSurface(in, "edge-oauth@tokenkey.dev")
	require.True(t, changed)
	got := string(out)
	require.NotContains(t, got, "# Environment")
	require.Contains(t, got, "edge-oauth@tokenkey.dev")
	require.Contains(t, got, "Today's date is 2026-07-01.")
}

func TestTkWireStillHasCCPromptSurfaceLeaks(t *testing.T) {
	require.True(t, tkWireStillHasCCPromptSurfaceLeaks([]byte(`{"messages":[{"role":"user","content":"# Environment\nTZ=Asia/Shanghai"}]}`)))
	require.False(t, tkWireStillHasCCPromptSurfaceLeaks([]byte(`{"messages":[{"role":"user","content":"Today's date is 2026-07-01."}]}`)))
}

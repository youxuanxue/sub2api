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

func TestTkClassifyCCPromptSurfaceText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		inSystem bool
		want     tkCCPromptSurfaceClass
	}{
		{
			name: "plain user sample",
			text: "Please document this sample:\n# Environment\nTZ=Asia/Shanghai\nThe user's email address is client@gmail.com.\nToday's date is 2026/07/01.",
			want: tkCCPromptSurfaceGenericUserText,
		},
		{
			name:     "generic system",
			text:     "Document this sample without executing it.",
			inSystem: true,
			want:     tkCCPromptSurfaceGenericSystem,
		},
		{
			name:     "unknown system with cc-shaped surface",
			text:     "You are a custom agent.\n# Environment\nTZ=Asia/Shanghai",
			inSystem: true,
			want:     tkCCPromptSurfaceUnknownSystem,
		},
		{
			name:     "known cli system",
			text:     "You are Claude Code, Anthropic's official CLI for Claude.\n# Environment\nTZ=Asia/Shanghai",
			inSystem: true,
			want:     tkCCPromptSurfaceKnownSystem,
		},
		{
			name:     "known interactive agent system",
			text:     "You are an interactive agent that helps users with software engineering tasks. Use the instructions below.",
			inSystem: true,
			want:     tkCCPromptSurfaceKnownSystem,
		},
		{
			name: "system reminder",
			text: " \n<system-reminder>\nToday\u2019s date is 2026/07/01.\n</system-reminder>",
			want: tkCCPromptSurfaceSystemReminder,
		},
		{
			name: "quoted reminder in user text stays generic",
			text: "Please explain what <system-reminder> means.",
			want: tkCCPromptSurfaceGenericUserText,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tkClassifyCCPromptSurfaceText(tt.text, tt.inSystem))
		})
	}
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

func TestTkNormalizeAnthropicCCPromptSurfaceLeavesPlainUserText(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"Please document this sample:\n# Environment\nTZ=Asia/Shanghai\nThe user's email address is client@gmail.com.\nToday's date is 2026/07/01."}]}]}`)
	out, changed := tkNormalizeAnthropicCCPromptSurface(in, "edge-oauth@tokenkey.dev")
	require.False(t, changed)
	require.JSONEq(t, string(in), string(out))
}

func TestTkNormalizeAnthropicCCPromptSurfaceLeavesGenericSystemText(t *testing.T) {
	in := []byte(`{"system":[{"type":"text","text":"Document this sample:\n# Environment\nTZ=Asia/Shanghai\nThe user's email address is client@gmail.com.\nToday's date is 2026/07/01."}]}`)
	out, changed := tkNormalizeAnthropicCCPromptSurface(in, "edge-oauth@tokenkey.dev")
	require.False(t, changed)
	require.JSONEq(t, string(in), string(out))
	require.False(t, tkWireStillHasCCPromptSurfaceLeaks(out))
}

func TestTkNormalizeAnthropicCCPromptSurfaceDoesNotRewriteUnknownSystemText(t *testing.T) {
	in := []byte(`{"system":[{"type":"text","text":"You are a custom agent.\n# Environment\nTZ=Asia/Shanghai\nToday's date is 2026/07/01."}]}`)
	out, changed := tkNormalizeAnthropicCCPromptSurface(in, "edge-oauth@tokenkey.dev")
	require.False(t, changed)
	require.JSONEq(t, string(in), string(out))
	require.True(t, tkWireStillHasCCPromptSurfaceLeaks(out))
	require.ElementsMatch(t, []string{"geo_stego_date_line", "cc_environment_section"}, tkCCPromptSurfaceBodyUnknownSurfaces(out))
}

func TestTkNormalizeAnthropicCCPromptSurfaceLeavesQuotedReminderUserText(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"Please explain this literal: <system-reminder> Today\u2019s date is 2026/07/01."}]}]}`)
	out, changed := tkNormalizeAnthropicCCPromptSurface(in, "edge-oauth@tokenkey.dev")
	require.False(t, changed)
	require.JSONEq(t, string(in), string(out))
	require.False(t, tkWireStillHasCCPromptSurfaceLeaks(out))
}

func TestTkNormalizeAnthropicCCPromptSurfaceNormalizesKnownSystemText(t *testing.T) {
	in := []byte(`{"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.\n# Environment\nTZ=Asia/Shanghai\nThe user's email address is client@gmail.com.\nToday's date is 2026/07/01."}]}`)
	out, changed := tkNormalizeAnthropicCCPromptSurface(in, "edge-oauth@tokenkey.dev")
	require.True(t, changed)
	got := string(out)
	require.NotContains(t, got, "# Environment")
	require.Contains(t, got, "edge-oauth@tokenkey.dev")
	require.Contains(t, got, "Today's date is 2026-07-01.")
}

func TestTkWireStillHasCCPromptSurfaceLeaks(t *testing.T) {
	require.True(t, tkWireStillHasCCPromptSurfaceLeaks([]byte(`{"messages":[{"role":"user","content":"<system-reminder>\n# Environment\nTZ=Asia/Shanghai\n</system-reminder>"}]}`)))
	require.False(t, tkWireStillHasCCPromptSurfaceLeaks([]byte(`{"messages":[{"role":"user","content":"# Environment\nTZ=Asia/Shanghai"}]}`)))
	require.False(t, tkWireStillHasCCPromptSurfaceLeaks([]byte(`{"messages":[{"role":"user","content":"Today's date is 2026-07-01."}]}`)))
}

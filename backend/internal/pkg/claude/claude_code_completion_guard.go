package claude

import "strings"

const (
	// ClaudeCodeCompletionGuardMarker stays wire-compatible with the original
	// OpenAI-compat-only guard so existing bridge detection keeps working while
	// Kiro and future Messages backends consume the same policy text.
	ClaudeCodeCompletionGuardMarker = "<sub2api-claude-code-todo-guard>"
	ClaudeCodeCompletionGuardText   = ClaudeCodeCompletionGuardMarker + `
You are operating as the model backend for Claude Code. Keep working in the same turn until the user's requested implementation and verification are complete. Do not send a final answer, completion claim, or progress summary as an end state while requested work remains.

For multi-step work, use the available task or todo tracking tools before editing and keep their state accurate. If any item remains in_progress or pending, continue using tools. Only stop early for a genuine blocker that requires user input, and state that blocker explicitly. Before ending, verify the requested result and leave no item in_progress.
</sub2api-claude-code-todo-guard>`
)

// EnsureClaudeCodeCompletionGuard appends the shared guard exactly once to an
// already-recognized Claude Code system prompt.
func EnsureClaudeCodeCompletionGuard(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if strings.Contains(prompt, ClaudeCodeCompletionGuardMarker) {
		return prompt
	}
	if prompt == "" {
		return ClaudeCodeCompletionGuardText
	}
	return prompt + "\n\n" + ClaudeCodeCompletionGuardText
}

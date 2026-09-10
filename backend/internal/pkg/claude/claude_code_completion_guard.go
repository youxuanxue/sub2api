package claude

const (
	// ClaudeCodeCompletionGuardMarker stays wire-compatible with the original
	// OpenAI-compatible Messages guard so existing bridge detection keeps working.
	ClaudeCodeCompletionGuardMarker = "<sub2api-claude-code-todo-guard>"
	// ClaudeCodeCompletionToolName is retained in the OpenAI-compatible guard
	// text for wire compatibility. Kiro no longer installs or consumes it.
	ClaudeCodeCompletionToolName  = "sub2apiClaudeCodeCompletion"
	ClaudeCodeCompletionGuardText = ClaudeCodeCompletionGuardMarker + `
You are operating as the model backend for Claude Code. Keep working in the same turn until the user's requested implementation and verification are complete. Do not send a final answer, completion claim, or progress summary as an end state while requested work remains.

For multi-step work, use the available task or todo tracking tools before editing and keep their state accurate. If any item remains in_progress or pending, continue using tools. Only stop early for a genuine blocker that requires user input, and state that blocker explicitly. Before ending, verify the requested result and leave no item in_progress.

If the transport-only ` + ClaudeCodeCompletionToolName + ` tool is available, it is the only valid way to finish a text-only response. Call it with status complete only after the requested work and verification are done, or with status blocked only when progress genuinely requires user input. Put the complete user-facing final answer or blocker question in its message argument. Do not call it for progress updates. If work remains, use the normal Claude Code tools instead.
</sub2api-claude-code-todo-guard>`
)

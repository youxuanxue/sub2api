//go:build unit

package kiro

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two client tool names that normalize to one Kiro name must fail the whole
// request. convertClaudeTools writes nameMap[normalized] = original, so the
// second declaration overwrites the first: the upstream receives two specs
// named "read" and the response path can only map that name back to one
// original, handing the client a tool_use for a name it never declared.
func TestValidateClaudeToolNames_RejectsCaseCollision(t *testing.T) {
	err := ValidateClaudeToolNames([]ClaudeTool{
		{Name: "Read"},
		{Name: "read"},
	})
	require.Error(t, err)

	var collision *ErrKiroToolNameCollision
	require.True(t, errors.As(err, &collision))
	assert.Equal(t, "read", collision.Normalized)
	assert.Contains(t, err.Error(), "Read")
	assert.Contains(t, err.Error(), "read")
	assert.Contains(t, err.Error(), "rename one of the tools")
}

// snake_case and camelCase spellings of one name also collide, because
// sanitizeToolName converts separators to camelCase boundaries.
func TestValidateClaudeToolNames_RejectsSnakeVersusCamelCollision(t *testing.T) {
	require.Error(t, ValidateClaudeToolNames([]ClaudeTool{
		{Name: "my_tool"},
		{Name: "myTool"},
	}))
	require.Error(t, ValidateClaudeToolNames([]ClaudeTool{
		{Name: "my-tool"},
		{Name: "my_tool"},
	}))
}

// The error must not depend on declaration order.
func TestValidateClaudeToolNames_CollisionMessageIsOrderStable(t *testing.T) {
	forward := ValidateClaudeToolNames([]ClaudeTool{{Name: "Read"}, {Name: "read"}})
	reverse := ValidateClaudeToolNames([]ClaudeTool{{Name: "read"}, {Name: "Read"}})
	require.Error(t, forward)
	require.Error(t, reverse)
	assert.Equal(t, forward.Error(), reverse.Error())
}

// Benign direction: distinct tools, repeated identical declarations, empty
// names, and an empty list must all pass silently.
func TestValidateClaudeToolNames_AcceptsNonColliding(t *testing.T) {
	require.NoError(t, ValidateClaudeToolNames(nil))
	require.NoError(t, ValidateClaudeToolNames([]ClaudeTool{
		{Name: "Read"},
		{Name: "Write"},
		{Name: "mcp__github__list_issues"},
		{Name: "bash"},
	}))
	// The same tool listed twice is not a collision: one original, one name.
	require.NoError(t, ValidateClaudeToolNames([]ClaudeTool{{Name: "read"}, {Name: "read"}}))
	require.NoError(t, ValidateClaudeToolNames([]ClaudeTool{{Name: ""}, {Name: "  "}}))
}

// A real request's worth of Claude Code built-ins must not start failing.
func TestValidateClaudeToolNames_AcceptsClaudeCodeBuiltinSet(t *testing.T) {
	names := []string{
		"Task", "Bash", "Glob", "Grep", "Read", "Edit", "Write",
		"NotebookEdit", "WebFetch", "WebSearch", "TodoWrite", "BashOutput", "KillShell",
	}
	tools := make([]ClaudeTool, 0, len(names))
	for _, name := range names {
		tools = append(tools, ClaudeTool{Name: name})
	}
	require.NoError(t, ValidateClaudeToolNames(tools))
}

func TestValidateOpenAIToolNames_RejectsCollisionAndSkipsNonFunctions(t *testing.T) {
	collide := []OpenAITool{{Type: "function"}, {Type: "function"}}
	collide[0].Function.Name = "read_file"
	collide[1].Function.Name = "readFile"
	require.Error(t, ValidateOpenAIToolNames(collide))

	ok := []OpenAITool{{Type: "function"}, {Type: "function"}}
	ok[0].Function.Name = "read_file"
	ok[1].Function.Name = "write_file"
	require.NoError(t, ValidateOpenAIToolNames(ok))

	// Non-function entries and empty names are skipped by convertOpenAITools,
	// so they must not fail validation either.
	skipped := []OpenAITool{{Type: "web_search"}, {Type: "function"}, {Type: "function"}}
	skipped[1].Function.Name = ""
	skipped[2].Function.Name = ""
	require.NoError(t, ValidateOpenAIToolNames(skipped))
}

// applyKiroToolNameRewrites must align prompt text and tool descriptions with
// the camelCase name actually advertised, not only length-truncated names.
func TestApplyKiroToolNameRewrites_RewritesCamelCaseRenames(t *testing.T) {
	specs, nameMap := convertClaudeTools([]ClaudeTool{
		{Name: "my_tool", Description: "call my_tool to do things"},
	})
	require.Equal(t, "myTool", specs[0].ToolSpecification.Name)

	payload := &KiroPayload{ToolNameMap: nameMap}
	payload.ConversationState.CurrentMessage.UserInputMessage.Content = "please use my_tool now"
	payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext =
		&UserInputMessageContext{Tools: specs}

	applyKiroToolNameRewrites(payload)

	current := payload.ConversationState.CurrentMessage.UserInputMessage
	assert.Equal(t, "please use myTool now", current.Content)
	assert.Equal(t, "call myTool to do things", current.UserInputMessageContext.Tools[0].ToolSpecification.Description)
}

// Benign direction: a case-only rename must NOT rewrite prose. Those names are
// ordinary English words, and a blanket replace corrupts sentences that merely
// mention the tool (locked by TestUS070_FileToolGuidanceScope).
func TestApplyKiroToolNameRewrites_SkipsCaseOnlyRenames(t *testing.T) {
	specs, nameMap := convertClaudeTools([]ClaudeTool{{Name: "Write", Description: "Write a file"}})
	require.Equal(t, "write", specs[0].ToolSpecification.Name)

	payload := &KiroPayload{ToolNameMap: nameMap}
	payload.ConversationState.CurrentMessage.UserInputMessage.Content =
		"Split large files across successive Write and Edit calls."
	payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext =
		&UserInputMessageContext{Tools: specs}

	applyKiroToolNameRewrites(payload)

	assert.Equal(t,
		"Split large files across successive Write and Edit calls.",
		payload.ConversationState.CurrentMessage.UserInputMessage.Content,
	)
}

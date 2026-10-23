//go:build unit

package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	claudepkg "github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

func TestApplyPromptFilters_ClaudeCodePreservesRules(t *testing.T) {
	ccPrompt := strings.Join([]string{
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"You are an interactive agent that helps users with software engineering tasks.",
		"# Environment",
		"gitStatus: dirty",
		"# doing tasks",
		"# using your tools",
		"# tone and style",
	}, "\n")

	got := applyPromptFilters(ccPrompt)
	require.Equal(t, strings.Replace(ccPrompt, "You are Claude Code, Anthropic's official CLI for Claude.", claudeCodeBackendPrompt, 1), got)
	require.NotContains(t, got, "You are Claude Code")
	require.Contains(t, got, "gitStatus: dirty")
}

// No gateway identity override. Ordinary caller text stays; recognized Claude Code
// identity lines are replaced while all caller rules remain.
func TestClaudeToKiro_PreservesCallerInstructions(t *testing.T) {
	const ccPrompt = "You are Claude Code, Anthropic's official CLI for Claude."
	for _, tc := range []struct {
		name       string
		system     any
		wantSystem string
	}{
		{name: "absent"},
		{name: "empty", system: ""},
		{name: "caller string", system: "Answer concisely.", wantSystem: "Answer concisely."},
		{name: "caller Claude Code", system: []any{map[string]any{"type": "text", "text": ccPrompt}}, wantSystem: claudeCodeBackendPrompt},
	} {
		for _, thinking := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/thinking=%t", tc.name, thinking), func(t *testing.T) {
				req := &ClaudeRequest{
					Model: "claude-sonnet-4-6", System: tc.system,
					Messages: []ClaudeMessage{
						{Role: "user", Content: "Remember the label alpha."},
						{Role: "assistant", Content: "The label is alpha."},
						{Role: "user", Content: "Reply exactly RELEASE_OK_7291."},
					},
				}
				before, err := json.Marshal(req)
				require.NoError(t, err)
				payload := ClaudeToKiro(req, thinking)
				model := MapModel(req.Model)
				user := func(text string) KiroHistoryMessage {
					return KiroHistoryMessage{UserInputMessage: &KiroUserInputMessage{Content: text, ModelID: model, Origin: "AI_EDITOR"}}
				}
				assistant := func(text string) KiroHistoryMessage {
					return KiroHistoryMessage{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: text}}
				}
				wantSystem := tc.wantSystem
				if thinking {
					wantSystem = ThinkingModePrompt
					if tc.wantSystem != "" {
						wantSystem += "\n\n" + tc.wantSystem
					}
				}
				wantHistory := []KiroHistoryMessage{}
				if wantSystem != "" {
					wantHistory = append(wantHistory, user(wantSystem), assistant("I will follow these instructions."))
				}
				wantHistory = append(wantHistory, user("Remember the label alpha."), assistant("The label is alpha."))
				require.Equal(t, wantHistory, payload.ConversationState.History)
				require.Equal(t, KiroUserInputMessage{Content: "Reply exactly RELEASE_OK_7291.", ModelID: model, Origin: "AI_EDITOR"}, payload.ConversationState.CurrentMessage.UserInputMessage)
				after, err := json.Marshal(req)
				require.NoError(t, err)
				require.JSONEq(t, string(before), string(after), "translation must not mutate caller input")
			})
		}
	}
}

func TestBuildClaudeSystemPrompt_NonClaudeCodeDoesNotAddCompletionGuard(t *testing.T) {
	got := buildClaudeSystemPrompt("You are a concise support assistant.", false, 0)
	require.NotContains(t, got, "<sub2api-claude-code-todo-guard>")
}

// No private completion protocol. Recognized Claude Code systems become the
// compact identity line; ordinary prompts and client tools stay untouched.
func TestClaudeToKiro_NoPrivateCompletionProtocol(t *testing.T) {
	base := "You are Claude Code, Anthropic's official CLI for Claude.\n# doing tasks\n# using your tools"
	for _, tc := range []struct {
		system      string
		wantPriming string
	}{
		{system: base, wantPriming: claudeCodeBackendPrompt + "\n# doing tasks\n# using your tools"},
		{system: base + "\n" + claudepkg.ClaudeCodeCompletionGuardText, wantPriming: claudeCodeBackendPrompt + "\n# doing tasks\n# using your tools\n" + claudepkg.ClaudeCodeCompletionGuardText},
		{system: "You are a concise support assistant.", wantPriming: "You are a concise support assistant."},
	} {
		payload := ClaudeToKiro(&ClaudeRequest{
			Model: "claude-opus-5", System: tc.system,
			Messages: []ClaudeMessage{{Role: "user", Content: "Answer only YES or NO. Do not call tools."}},
			Tools:    []ClaudeTool{{Name: "Read", Description: "Read a file", InputSchema: map[string]interface{}{"type": "object"}}},
		}, false)
		current := payload.ConversationState.CurrentMessage.UserInputMessage
		require.Equal(t, "Answer only YES or NO. Do not call tools.", current.Content)
		require.NotNil(t, current.UserInputMessageContext)
		require.Len(t, current.UserInputMessageContext.Tools, 1)
		require.NotEqual(t, claudepkg.ClaudeCodeCompletionToolName, current.UserInputMessageContext.Tools[0].ToolSpecification.Name)
		require.Equal(t, tc.wantPriming, payload.ConversationState.History[0].UserInputMessage.Content)
		require.NotContains(t, payload.ConversationState.History[0].UserInputMessage.Content, "Continue the same task now")
	}
}

func TestKiroReliabilityPreservesProjectRules(t *testing.T) {
	const rules = "PROJECT_RULES: 保留已有文件；禁止重新初始化项目。"
	const base = "You are Claude Code, Anthropic's official CLI for Claude.\n# doing tasks\nFollow existing project conventions."
	for _, system := range []any{base + "\n" + rules, []any{map[string]any{"type": "text", "text": base}, map[string]any{"type": "text", "text": rules}}} {
		got := buildClaudeSystemPrompt(system, false, 0)
		require.Contains(t, got, rules)
		require.Contains(t, got, "Follow existing project conventions.")
	}
}

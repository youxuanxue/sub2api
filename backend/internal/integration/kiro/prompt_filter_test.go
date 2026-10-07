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

func TestApplyPromptFilters_ClaudeCodePreservesAnthropicIdentity(t *testing.T) {
	ccPrompt := strings.Join([]string{
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"You are an interactive agent that helps users with software engineering tasks.",
		"# doing tasks",
		"# using your tools",
		"# tone and style",
	}, "\n")

	got := applyPromptFilters(ccPrompt)
	require.Contains(t, got, "You are Claude Code, Anthropic's official CLI for Claude.")
	require.Contains(t, got, "# doing tasks")
	require.NotContains(t, got, "backend for Claude Code CLI")
	require.NotEqual(t, claudeCodeBackendPrompt, got)
}

func TestApplyPromptFilters_ClaudeCodeStripsEnvNoise(t *testing.T) {
	ccPrompt := strings.Join([]string{
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"You are an interactive agent that helps users with software engineering tasks.",
		"# Environment",
		"gitStatus: dirty",
		"# doing tasks",
		"# using your tools",
	}, "\n")

	got := applyPromptFilters(ccPrompt)
	require.Contains(t, got, "Anthropic's official CLI for Claude")
	require.NotContains(t, got, "gitStatus")
	require.NotContains(t, got, "# Environment")
}

// Client instructions belong to the caller. Adding an identity override here
// caused Kiro to refuse otherwise valid requests with END_TURN and HTTP 200.
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
		{name: "caller Claude Code", system: []any{map[string]any{"type": "text", "text": ccPrompt}}, wantSystem: ccPrompt},
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
	got := buildClaudeSystemPrompt("You are a concise support assistant.", false)
	require.NotContains(t, got, "<sub2api-claude-code-todo-guard>")
}

// The gateway must preserve client intent, without injecting business completion
// instructions or tools. Client-supplied instructions remain client-owned.
func TestClaudeToKiro_NoPrivateCompletionProtocol(t *testing.T) {
	base := "You are Claude Code, Anthropic's official CLI for Claude.\n# doing tasks\n# using your tools"
	for _, system := range []string{base, base + "\n" + claudepkg.ClaudeCodeCompletionGuardText, "You are a concise support assistant."} {
		payload := ClaudeToKiro(&ClaudeRequest{
			Model: "claude-opus-5", System: system,
			Messages: []ClaudeMessage{{Role: "user", Content: "Answer only YES or NO. Do not call tools."}},
			Tools:    []ClaudeTool{{Name: "Read", Description: "Read a file", InputSchema: map[string]interface{}{"type": "object"}}},
		}, false)
		current := payload.ConversationState.CurrentMessage.UserInputMessage
		require.Equal(t, "Answer only YES or NO. Do not call tools.", current.Content)
		require.NotNil(t, current.UserInputMessageContext)
		require.Len(t, current.UserInputMessageContext.Tools, 1)
		require.NotEqual(t, claudepkg.ClaudeCodeCompletionToolName, current.UserInputMessageContext.Tools[0].ToolSpecification.Name)
		priming := payload.ConversationState.History[0].UserInputMessage.Content
		require.Equal(t, strings.Count(system, claudepkg.ClaudeCodeCompletionGuardMarker), strings.Count(priming, claudepkg.ClaudeCodeCompletionGuardMarker))
		require.NotContains(t, priming, "Continue the same task now")
	}
}

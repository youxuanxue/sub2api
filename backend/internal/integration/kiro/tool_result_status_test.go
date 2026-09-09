//go:build unit

package kiro

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Exercise the public translator from Anthropic JSON, including the paths that
// move a tool result out of the active structured turn.
func TestClaudeToKiro_PreservesToolResultFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		field  string
		status string
	}{
		{name: "failed", field: `,"is_error":true`, status: "error"},
		{name: "successful", field: `,"is_error":false`, status: "success"},
		{name: "omitted_is_success", status: "success"},
	} {
		for _, phase := range []string{"active", "history", "orphan", "completion_continuation"} {
			t.Run(tc.name+"/"+phase, func(t *testing.T) {
				var req ClaudeRequest
				require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{
					"model":"claude-sonnet-4-5","max_tokens":128,
					"messages":[
						{"role":"user","content":"Read the fixture"},
						{"role":"assistant","content":[{"type":"tool_use","id":"toolu_fixture","name":"Read","input":{"file_path":"fixture.txt"}}]},
						{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_fixture","content":"fixture result"%s}]}
					]
				}`, tc.field)), &req))
				switch phase {
				case "history":
					req.Messages = append(req.Messages,
						ClaudeMessage{Role: "assistant", Content: "I received the result."},
						ClaudeMessage{Role: "user", Content: "What happened?"})
				case "orphan":
					req.Messages = req.Messages[2:]
				}
				payload := ClaudeToKiro(&req, false)
				if phase == "completion_continuation" {
					payload.ClaudeCodeCompletionProtocol = true
					PrepareClaudeCodeCompletionContinuation(payload, "I received the result.")
				}
				current := payload.ConversationState.CurrentMessage.UserInputMessage
				if phase == "active" {
					require.NotNil(t, current.UserInputMessageContext)
					results := current.UserInputMessageContext.ToolResults
					require.Len(t, results, 1)
					require.Equal(t, tc.status, results[0].Status)
					require.Equal(t, "toolu_fixture", results[0].ToolUseID)
					require.Equal(t, "fixture result", results[0].Content[0].Text)
					return
				}
				var resultText string
				if phase == "orphan" {
					resultText = current.Content
				} else {
					for _, message := range payload.ConversationState.History {
						if message.UserInputMessage != nil {
							resultText += message.UserInputMessage.Content + "\n"
						}
					}
				}
				require.Contains(t, resultText, "fixture result")
				if tc.status == "error" {
					require.Contains(t, resultText, "[Tool execution failed]")
				} else {
					require.NotContains(t, resultText, "[Tool execution failed]")
				}
			})
		}
	}
}

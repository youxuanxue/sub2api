//go:build unit

package kiro

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUS070_FileToolGuidanceScope(t *testing.T) {
	for _, names := range [][]string{nil, {"Read"}, {"write"}, {"mcp__files__Write"}, {"Write"}, {"Edit"}, {"MultiEdit"}, {"Write", "Edit", "MultiEdit"}} {
		for _, enabled := range []bool{false, true} {
			for _, thinking := range []bool{false, true} {
				for _, system := range []string{"", "Preserve completed work.", "You are Claude Code, Anthropic's official CLI."} {
					t.Run(fmt.Sprintf("%v/enabled=%t/thinking=%t/system=%s", names, enabled, thinking, system), func(t *testing.T) {
						req := &ClaudeRequest{Model: "claude-opus-4-6", System: system, Messages: []ClaudeMessage{{Role: "user", Content: "Append the next chunk."}}}
						wantGuidance := false
						for _, name := range names {
							req.Tools = append(req.Tools, ClaudeTool{Name: name, Description: "Customer tool", InputSchema: map[string]any{"type": "object"}})
							if name == "Write" || name == "Edit" || name == "MultiEdit" {
								wantGuidance = enabled
							}
						}
						before, err := json.Marshal(req)
						require.NoError(t, err)
						baseline := ClaudeToKiro(req, thinking)
						got := ClaudeToKiroWithFileToolGuidance(req, thinking, enabled)
						// A new transport continuation ID is expected on every conversion.
						baseline.ConversationState.AgentContinuationId = got.ConversationState.AgentContinuationId
						if !wantGuidance {
							require.Equal(t, baseline, got)
						} else {
							wantSystem := buildClaudeSystemPrompt(system, thinking)
							if wantSystem != "" {
								wantSystem += "\n\n"
							}
							wantSystem += fileToolChunkingGuidance
							require.Equal(t, wantSystem, got.ConversationState.History[0].UserInputMessage.Content)
							require.Equal(t, baseline.ConversationState.CurrentMessage, got.ConversationState.CurrentMessage)
							require.Equal(t, baseline.ToolNameMap, got.ToolNameMap)
							require.NotEqual(t, baseline.ConversationState.ConversationID, got.ConversationState.ConversationID)
							again := ClaudeToKiroWithFileToolGuidance(req, thinking, enabled)
							require.Equal(t, got.ConversationState.ConversationID, again.ConversationState.ConversationID)
							require.Equal(t, got.ConversationState.History, again.ConversationState.History)
						}
						after, err := json.Marshal(req)
						require.NoError(t, err)
						require.JSONEq(t, string(before), string(after))
					})
				}
			}
		}
	}
}

func TestUS070_FileToolGuidancePreservesHistory(t *testing.T) {
	var req ClaudeRequest
	require.NoError(t, json.Unmarshal([]byte(`{
 "model":"claude-opus-4-6", "system":"Preserve finished files.",
 "tools":[{"name":"Edit","input_schema":{"type":"object"}}],
 "messages":[
 {"role":"user","content":"Append to finished.txt"},
 {"role":"assistant","content":[{"type":"tool_use","id":"edit-1","name":"Edit","input":{"file_path":"finished.txt","old_string":"中文🙂","new_string":"中文🙂\nnext"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"edit-1","content":"permission denied","is_error":true}]},
 {"role":"system","content":"Do not retry the denied edit."},
 {"role":"assistant","content":"Waiting for instructions."},
 {"role":"user","content":"Report status. Do not execute tools."}
 ]}`), &req))
	baseline := ClaudeToKiro(&req, false)
	got := ClaudeToKiroWithFileToolGuidance(&req, false, true)
	require.Equal(t, baseline.ConversationState.History[1:], got.ConversationState.History[1:])
	require.Equal(t, baseline.ConversationState.CurrentMessage, got.ConversationState.CurrentMessage)
	require.Equal(t, baseline.InferenceConfig, got.InferenceConfig)
	require.Equal(t, baseline.ToolNameMap, got.ToolNameMap)
}

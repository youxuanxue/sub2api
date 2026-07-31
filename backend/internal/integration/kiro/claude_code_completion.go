package kiro

import (
	"strings"

	claudepkg "github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/google/uuid"
)

const claudeCodeCompletionContinuationPrompt = `The preceding assistant response ended without the required transport completion signal, so the Claude Code turn is still active. Continue the same task now. This repair turn is transport-only unless you call a normal Claude Code tool. Do not emit a recap or repeat or restate text from the preceding assistant response. Use the normal Claude Code tools for every remaining action. If the preceding response already completed the requested work and verification, emit no additional assistant text and call sub2apiClaudeCodeCompletion with status complete, reusing the preceding user-facing answer verbatim as its message. If progress genuinely requires user input, call it with status blocked and the exact blocker question.`

// ClaudeCodeCompletionSignal is emitted by the transport-private completion
// tool. The Kiro gateway consumes it and never exposes the tool call itself to
// Claude Code.
type ClaudeCodeCompletionSignal struct {
	Status  string
	Message string
}

// IsClaudeCodeCompletionToolUse reports whether a Kiro tool call belongs to
// the transport-private completion protocol.
func IsClaudeCodeCompletionToolUse(toolUse KiroToolUse) bool {
	return toolUse.Name == claudepkg.ClaudeCodeCompletionToolName
}

// addClaudeCodeCompletionTool installs the transport-private completion tool.
// A client tool collision disables the protocol instead of shadowing a real
// Claude Code tool.
func addClaudeCodeCompletionTool(tools []KiroToolWrapper) ([]KiroToolWrapper, bool) {
	for i := range tools {
		if tools[i].ToolSpecification.Name == claudepkg.ClaudeCodeCompletionToolName {
			return tools, false
		}
	}

	tool := KiroToolWrapper{}
	tool.ToolSpecification.Name = claudepkg.ClaudeCodeCompletionToolName
	tool.ToolSpecification.Description = "Report that the Claude Code task is complete or genuinely blocked on user input. This transport-only tool is the only valid way to end a text-only response. Do not call it while implementation, investigation, or verification remains."
	tool.ToolSpecification.InputSchema = InputSchema{JSON: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status": map[string]any{
				"type":        "string",
				"enum":        []string{"complete", "blocked"},
				"description": "Use complete only when all requested work and verification are done; use blocked only when user input is required.",
			},
			"message": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "The complete final answer shown to the user, or the exact blocker and question requiring user input.",
			},
		},
		"required": []string{"status", "message"},
	}}
	return append(tools, tool), true
}

// ConsumeClaudeCodeCompletionSignal removes transport-private tool calls and
// returns the first valid completion signal. Ordinary Claude Code tool calls
// are preserved byte-for-byte for the Anthropic response.
func ConsumeClaudeCodeCompletionSignal(toolUses []KiroToolUse) ([]KiroToolUse, *ClaudeCodeCompletionSignal) {
	clientToolUses := make([]KiroToolUse, 0, len(toolUses))
	var signal *ClaudeCodeCompletionSignal
	for i := range toolUses {
		toolUse := toolUses[i]
		if !IsClaudeCodeCompletionToolUse(toolUse) {
			clientToolUses = append(clientToolUses, toolUse)
			continue
		}
		if signal != nil {
			continue
		}
		status, _ := toolUse.Input["status"].(string)
		status = strings.ToLower(strings.TrimSpace(status))
		if status != "complete" && status != "blocked" {
			continue
		}
		message, _ := toolUse.Input["message"].(string)
		signal = &ClaudeCodeCompletionSignal{
			Status:  status,
			Message: strings.TrimSpace(message),
		}
	}
	return clientToolUses, signal
}

// PrepareClaudeCodeCompletionContinuation turns the just-finished Kiro model
// response into history and appends a private continuation instruction as the
// next current message. It keeps the conversation ID and normal tool catalog,
// but rotates the continuation ID and removes stale images/tool results.
func PrepareClaudeCodeCompletionContinuation(payload *KiroPayload, assistantText string) {
	if payload == nil || !payload.ClaudeCodeCompletionProtocol {
		return
	}

	current := payload.ConversationState.CurrentMessage.UserInputMessage
	tools := []KiroToolWrapper(nil)
	if current.UserInputMessageContext != nil {
		tools = current.UserInputMessageContext.Tools
	}

	history := append(payload.ConversationState.History, KiroHistoryMessage{
		UserInputMessage: &current,
	})
	assistantText = strings.TrimSpace(assistantText)
	if assistantText == "" {
		assistantText = "[No completion signal was provided.]"
	}
	history = append(history, KiroHistoryMessage{
		AssistantResponseMessage: &KiroAssistantResponseMessage{Content: assistantText},
	})
	payload.ConversationState.History = sanitizeKiroHistory(history, nil)

	next := KiroUserInputMessage{
		Content: claudeCodeCompletionContinuationPrompt,
		ModelID: current.ModelID,
		Origin:  current.Origin,
	}
	if len(tools) > 0 {
		next.UserInputMessageContext = &UserInputMessageContext{Tools: tools}
	}
	payload.ConversationState.CurrentMessage.UserInputMessage = next
	payload.ConversationState.AgentContinuationId = uuid.New().String()
	truncatePayloadToLimit(payload, true)
}

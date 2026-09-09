package kiro

import "strings"

// normalizeKiroToolHistory preserves the CLI's structured assistant/user tool
// pairs. Only results without a preceding call need a text fallback.
func normalizeKiroToolHistory(payload *KiroPayload) {
	current := &payload.ConversationState.CurrentMessage.UserInputMessage
	history := make([]KiroHistoryMessage, 0, len(payload.ConversationState.History))
	for _, msg := range payload.ConversationState.History {
		if a := msg.AssistantResponseMessage; a != nil {
			a.Content = stripPollutedToolCallText(a.Content)
			if len(a.ToolUses) == 0 && (strings.TrimSpace(a.Content) == "" || strings.TrimSpace(a.Content) == minimalFallbackUserContent) {
				continue
			}
			if len(history) == 0 {
				continue
			}
			if prev := history[len(history)-1].AssistantResponseMessage; prev != nil {
				prev.Content = joinHistoryText(prev.Content, a.Content)
				prev.ToolUses = append(prev.ToolUses, a.ToolUses...)
				continue
			}
		}
		if u := msg.UserInputMessage; u != nil {
			if u.UserInputMessageContext != nil {
				u.UserInputMessageContext.Tools = nil
			}
			if len(history) > 0 && history[len(history)-1].UserInputMessage != nil {
				mergeKiroUserMessages(history[len(history)-1].UserInputMessage, u)
				continue
			}
		}
		history = append(history, msg)
	}
	// Consecutive user blocks (including the current message) are one logical
	// result batch. Preserve all IDs and images when joining them.
	if len(history) > 0 && history[len(history)-1].UserInputMessage != nil {
		previous := history[len(history)-1].UserInputMessage
		mergeKiroUserMessages(previous, current)
		current.Content = previous.Content
		current.Images = previous.Images
		current.UserInputMessageContext = previous.UserInputMessageContext
		history = history[:len(history)-1]
	}
	var previous *KiroAssistantResponseMessage
	for _, msg := range history {
		if msg.UserInputMessage != nil {
			repairKiroToolResultPair(previous, msg.UserInputMessage)
			previous = nil
		} else {
			previous = msg.AssistantResponseMessage
		}
	}
	repairKiroToolResultPair(previous, current)
	payload.ConversationState.History = history
}

func mergeKiroUserMessages(dst, src *KiroUserInputMessage) {
	dst.Content = joinHistoryText(dst.Content, src.Content)
	dst.Images = append(dst.Images, src.Images...)
	if ctx := src.UserInputMessageContext; ctx != nil {
		if dst.UserInputMessageContext == nil {
			dst.UserInputMessageContext = &UserInputMessageContext{}
		}
		dst.UserInputMessageContext.ToolResults = append(dst.UserInputMessageContext.ToolResults, ctx.ToolResults...)
		dst.UserInputMessageContext.Tools = ctx.Tools
	}
}

func repairKiroToolResultPair(previous *KiroAssistantResponseMessage, user *KiroUserInputMessage) {
	var calls []KiroToolUse
	if previous != nil {
		calls = previous.ToolUses
	}
	pending := make(map[string]bool, len(calls))
	for _, call := range calls {
		pending[call.ToolUseID] = true
	}
	ctx := user.UserInputMessageContext
	if ctx == nil {
		ctx = &UserInputMessageContext{}
	}
	results := make([]KiroToolResult, 0, len(ctx.ToolResults))
	var orphans []KiroToolResult
	for _, result := range ctx.ToolResults {
		if pending[result.ToolUseID] && result.ToolUseID != "" {
			results = append(results, result)
			delete(pending, result.ToolUseID)
		} else {
			orphans = append(orphans, result)
		}
	}
	for _, call := range calls {
		if pending[call.ToolUseID] {
			// This is a protocol repair, not evidence that a tool ran or that the
			// user cancelled it. Never infer success from a missing client result.
			results = append(results, KiroToolResult{
				ToolUseID: call.ToolUseID,
				Status:    "error",
				Content:   []KiroResultContent{{Text: "The client did not provide a result for this tool call. Execution is not confirmed."}},
			})
			delete(pending, call.ToolUseID)
		}
	}
	// In the live runtime probe, status=error alone was read as success when
	// content was neutral. Carry failure inside the tool block as well.
	for i := range results {
		result := &results[i]
		if result.Status == "error" {
			result.Content = append([]KiroResultContent(nil), result.Content...)
			if len(result.Content) == 0 {
				result.Content = []KiroResultContent{{}}
			}
			result.Content[0].Text = preserveToolResultFailure(*result, result.Content[0].Text)
		}
	}
	ctx.ToolResults = results
	user.Content = joinHistoryText(user.Content, narrateToolResults(orphans))
	if len(ctx.Tools) > 0 || len(ctx.ToolResults) > 0 {
		user.UserInputMessageContext = ctx
	} else {
		user.UserInputMessageContext = nil
	}
	if strings.TrimSpace(user.Content) == "" && len(results) == 0 {
		user.Content = normalizeUserContent("", len(user.Images) > 0)
		if user.Content == "" {
			user.Content = minimalFallbackUserContent
		}
	}
}

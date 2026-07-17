package service

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TokenKey: proactive pre-filter for Claude Code ToolSearch + signed thinking storms.
//
// anthropics/claude-code#63792 (and the generic #10199 "Thinking Block Modification
// Error"): when dynamic/on-demand tool loading (ToolSearch) is active, Claude Code
// re-serializes prior assistant turns after the tool set changes. That perturbs
// signed thinking/redacted_thinking blocks in historical messages; Anthropic rejects
// the request with 400 "... cannot be modified". The client then strips the blocks
// and retries — an extra full round-trip on every poisoned turn.
//
// TokenKey repairs PRE-FLIGHT (same net body shape as the client's post-400 retry,
// without the wasted upstream call). Only historical assistant turns are touched;
// an assistant-prefill final message is left intact for the interleaved-thinking
// structural constraint.

func tkBodyHasToolSearchTools(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	if !bytes.Contains(body, []byte("tool_search")) {
		return false
	}
	for _, t := range tools.Array() {
		typ := strings.ToLower(t.Get("type").String())
		if strings.Contains(typ, "tool_search") {
			return true
		}
	}
	return false
}

func tkBodyHasSignedThinkingHistory(body []byte) bool {
	msgs := gjson.GetBytes(body, "messages")
	if !msgs.IsArray() {
		return false
	}
	arr := msgs.Array()
	if len(arr) == 0 {
		return false
	}
	// Historical = any assistant turn before the last message, or all assistant
	// turns when the last message is not an assistant prefill.
	lastIsAssistantPrefill := arr[len(arr)-1].Get("role").String() == "assistant"
	end := len(arr)
	if lastIsAssistantPrefill {
		end--
	}
	for i := 0; i < end; i++ {
		if arr[i].Get("role").String() != "assistant" {
			continue
		}
		content := arr[i].Get("content")
		if !content.IsArray() {
			continue
		}
		for _, block := range content.Array() {
			typ := block.Get("type").String()
			if typ != "thinking" && typ != "redacted_thinking" {
				continue
			}
			if typ == "thinking" {
				if sig := block.Get("signature").String(); sig != "" {
					return true
				}
				continue
			}
			if data := block.Get("data").String(); data != "" {
				return true
			}
		}
	}
	return false
}

// TkPrefilterToolSearchHistoricalThinking downgrades signed thinking blocks in
// historical assistant turns when ToolSearch tools are present. Returns the input
// unchanged when the gate does not apply or when no modification is needed.
func TkPrefilterToolSearchHistoricalThinking(body []byte, mappedModel string) []byte {
	if !ShouldApplyRetryFilters(mappedModel) {
		return body
	}
	if !tkBodyHasToolSearchTools(body) || !tkBodyHasSignedThinkingHistory(body) {
		return body
	}
	return tkStripHistoricalAssistantThinking(body)
}

func tkStripHistoricalAssistantThinking(body []byte) []byte {
	jsonStr := string(body)
	msgsRes := gjson.Get(jsonStr, "messages")
	if !msgsRes.Exists() || !msgsRes.IsArray() {
		return body
	}

	var messages []any
	if err := json.Unmarshal(sliceRawFromBody(body, msgsRes), &messages); err != nil {
		return body
	}
	if len(messages) == 0 {
		return body
	}

	prefillIdx := -1
	if lastMap, ok := messages[len(messages)-1].(map[string]any); ok {
		if role, _ := lastMap["role"].(string); role == "assistant" {
			prefillIdx = len(messages) - 1
		}
	}

	topLevelThinkingExists := gjson.Get(jsonStr, "thinking").Exists()
	anyThinkingPreserved := false
	modified := false

	for i := 0; i < len(messages); i++ {
		if i == prefillIdx {
			continue
		}
		msgMap, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)
		if role != "assistant" {
			continue
		}
		content, ok := msgMap["content"].([]any)
		if !ok {
			continue
		}

		var newContent []any
		modifiedThisMsg := false
		for bi, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				if newContent != nil {
					newContent = append(newContent, block)
				}
				continue
			}
			blockType, _ := blockMap["type"].(string)
			switch blockType {
			case "thinking", "redacted_thinking":
				modifiedThisMsg = true
				if newContent == nil {
					newContent = make([]any, 0, len(content))
					newContent = append(newContent, content[:bi]...)
				}
				if blockType == "thinking" {
					if thinkingText, _ := blockMap["thinking"].(string); thinkingText != "" {
						newContent = append(newContent, map[string]any{"type": "text", "text": thinkingText})
					}
				}
				continue
			}
			if newContent != nil {
				newContent = append(newContent, block)
			}
		}

		if !modifiedThisMsg {
			continue
		}
		modified = true
		if len(newContent) == 0 {
			msgMap["content"] = []any{map[string]any{"type": "text", "text": "(assistant content removed)"}}
			continue
		}
		msgMap["content"] = newContent
	}

	if prefillIdx >= 0 {
		if msgMap, ok := messages[prefillIdx].(map[string]any); ok {
			if content, ok := msgMap["content"].([]any); ok {
				for _, block := range content {
					if bm, ok := block.(map[string]any); ok {
						if t, _ := bm["type"].(string); t == "thinking" || t == "redacted_thinking" {
							anyThinkingPreserved = true
							break
						}
					}
				}
			}
		}
	}

	deleteTopLevelThinking := topLevelThinkingExists && !anyThinkingPreserved
	if !modified && !deleteTopLevelThinking {
		return body
	}

	out := body
	if deleteTopLevelThinking {
		if b, err := sjson.DeleteBytes(out, "thinking"); err == nil {
			out = removeThinkingDependentContextStrategies(b)
		} else {
			return body
		}
	}
	if modified {
		msgsBytes, err := json.Marshal(messages)
		if err != nil {
			return body
		}
		var setErr error
		out, setErr = sjson.SetRawBytes(out, "messages", msgsBytes)
		if setErr != nil {
			return body
		}
	}

	logger.LegacyPrintf("service.gateway",
		"[ToolSearchThinkingPrefilter] stripped historical signed thinking blocks before upstream forward (claude-code #63792)")
	return out
}

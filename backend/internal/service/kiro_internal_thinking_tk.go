package service

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	kiroInternalThinkingGinKey = "ops_kiro_internal_thinking_blocks"
)

// captureKiroInternalThinking captures upstream reasoning for QA.
func captureKiroInternalThinking(c *gin.Context, thinking, signature string) {
	stashKiroInternalThinkingBlocks(c, thinking, signature)
}

// kiroInternalThinkingBlockJSON returns one Anthropic-shaped thinking block JSON
// string for prod QA raw capture. Signature is included only when Kiro upstream sent it.
func kiroInternalThinkingBlockJSON(thinking, signature string) string {
	thinking = strings.TrimSpace(thinking)
	signature = strings.TrimSpace(signature)
	if thinking == "" && signature == "" {
		return ""
	}
	block := map[string]any{"type": "thinking"}
	if thinking != "" {
		block["thinking"] = thinking
	}
	if signature != "" {
		block["signature"] = signature
	}
	b, err := json.Marshal(block)
	if err != nil {
		return ""
	}
	return string(b)
}

func kiroInternalThinkingBlocksFromCapture(thinking, signature string) []string {
	block := kiroInternalThinkingBlockJSON(thinking, signature)
	if block == "" {
		return nil
	}
	return []string{block}
}

func stashKiroInternalThinkingBlocks(c *gin.Context, thinking, signature string) {
	if c == nil {
		return
	}
	blocks := kiroInternalThinkingBlocksFromCapture(thinking, signature)
	if len(blocks) == 0 {
		return
	}
	applyKiroInternalThinkingBlocks(c, blocks)
}

func applyKiroInternalThinkingBlocks(c *gin.Context, blocks []string) {
	if c == nil || len(blocks) == 0 {
		return
	}
	existing, _ := c.Get(kiroInternalThinkingGinKey)
	if prior, ok := existing.([]string); ok && len(prior) > 0 {
		blocks = append(append([]string{}, prior...), blocks...)
	}
	blocks = consolidateKiroInternalThinkingBlocks(blocks)
	if len(blocks) == 0 {
		return
	}
	c.Set(kiroInternalThinkingGinKey, blocks)
}

// consolidateKiroInternalThinkingBlocks merges multiple Anthropic-shaped thinking
// blocks (e.g. internal thinking replays or split reasoning frames) into one
// block so QA/traj always see thinking text and signature together.
func consolidateKiroInternalThinkingBlocks(blocks []string) []string {
	if len(blocks) == 0 {
		return nil
	}
	var thinkingParts []string
	signature := ""
	for _, raw := range blocks {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
			continue
		}
		if strings.TrimSpace(anyString(obj["type"])) != "thinking" {
			continue
		}
		if t := strings.TrimSpace(anyString(obj["thinking"])); t != "" {
			thinkingParts = append(thinkingParts, t)
		}
		if sig := strings.TrimSpace(anyString(obj["signature"])); sig != "" && signature == "" {
			signature = sig
		}
	}
	merged := kiroInternalThinkingBlockJSON(strings.Join(thinkingParts, ""), signature)
	if merged == "" {
		return nil
	}
	return []string{merged}
}

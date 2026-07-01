package service

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	kiroInternalThinkingGinKey         = "ops_kiro_internal_thinking_blocks"
	kiroInternalThinkingResponseHeader = "X-Tk-Internal-Thinking-Blocks"
	kiroInternalThinkingSSECommentPfx  = ": x-tk-internal-thinking "
)

// kiroInternalThinkingBlockJSON returns one Anthropic-shaped thinking block JSON
// string for QA/traj export. Kiro upstream has no signature token; only plaintext
// thinking is stashed (client-facing wire stays redacted_thinking).
func kiroInternalThinkingBlockJSON(thinking string) string {
	thinking = strings.TrimSpace(thinking)
	if thinking == "" {
		return ""
	}
	b, err := json.Marshal(map[string]any{
		"type":     "thinking",
		"thinking": thinking,
	})
	if err != nil {
		return ""
	}
	return string(b)
}

func kiroInternalThinkingBlocksFromPlaintext(thinking string) []string {
	block := kiroInternalThinkingBlockJSON(thinking)
	if block == "" {
		return nil
	}
	return []string{block}
}

func encodeKiroInternalThinkingPayload(blocks []string) string {
	if len(blocks) == 0 {
		return ""
	}
	b, err := json.Marshal(blocks)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

func decodeKiroInternalThinkingPayload(encoded string) []string {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	var blocks []string
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		trimmed := strings.TrimSpace(block)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stashKiroInternalThinkingBlocks(c *gin.Context, thinking string) {
	if c == nil {
		return
	}
	blocks := kiroInternalThinkingBlocksFromPlaintext(thinking)
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
	c.Set(kiroInternalThinkingGinKey, blocks)
}

func writeKiroInternalThinkingResponseHeader(hdr http.Header, thinking string) {
	if hdr == nil {
		return
	}
	payload := encodeKiroInternalThinkingPayload(kiroInternalThinkingBlocksFromPlaintext(thinking))
	if payload == "" {
		return
	}
	hdr.Set(kiroInternalThinkingResponseHeader, payload)
}

func writeKiroInternalThinkingSSEComment(w io.Writer, thinking string) error {
	payload := encodeKiroInternalThinkingPayload(kiroInternalThinkingBlocksFromPlaintext(thinking))
	if payload == "" || w == nil {
		return nil
	}
	_, err := io.WriteString(w, kiroInternalThinkingSSECommentPfx+payload+"\n\n")
	return err
}

func kiroInternalThinkingBlocksFromUpstream(hdr http.Header) []string {
	if hdr == nil {
		return nil
	}
	return decodeKiroInternalThinkingPayload(hdr.Get(kiroInternalThinkingResponseHeader))
}

func parseKiroInternalThinkingSSECommentLine(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, strings.TrimSpace(kiroInternalThinkingSSECommentPfx)) {
		return nil, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, strings.TrimSpace(kiroInternalThinkingSSECommentPfx)))
	blocks := decodeKiroInternalThinkingPayload(payload)
	if len(blocks) == 0 {
		return nil, false
	}
	return blocks, true
}

func applyKiroInternalThinkingFromUpstream(c *gin.Context, hdr http.Header) {
	blocks := kiroInternalThinkingBlocksFromUpstream(hdr)
	if len(blocks) == 0 {
		return
	}
	applyKiroInternalThinkingBlocks(c, blocks)
}

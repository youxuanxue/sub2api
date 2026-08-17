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
	// kiroInternalThinkingMirrorHopRequestHeader is set by prod mirror passthrough
	// on outbound edge hops so Edge emits the side channel on the wire. Edge
	// direct clients must not receive plaintext thinking on the response body.
	kiroInternalThinkingMirrorHopRequestHeader = "X-Tk-Internal-Thinking-Relay"
)

func kiroInternalThinkingMirrorHopRequested(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return strings.TrimSpace(c.GetHeader(kiroInternalThinkingMirrorHopRequestHeader)) != ""
}

func setKiroInternalThinkingMirrorHopHeader(hdr http.Header) {
	if hdr == nil {
		return
	}
	hdr.Set(kiroInternalThinkingMirrorHopRequestHeader, "1")
}

// setKiroInternalThinkingMirrorHopHeaderForAccount marks prod→edge Kiro mirror hops.
// Only prod Anthropic API-key stubs that represent a downstream edge Kiro pool emit
// the header; native Anthropic edge relays (cc-us5/cc-us6) must not, or edge may
// treat Fable/Opus requests as Kiro-sidecar traffic and reject them.
func setKiroInternalThinkingMirrorHopHeaderForAccount(hdr http.Header, account *Account) {
	if hdr == nil || account == nil || account.Type != AccountTypeAPIKey {
		return
	}
	if !account.IsKiroMirrorStub() {
		return
	}
	if strings.TrimSpace(account.GetCredential("base_url")) == "" {
		return
	}
	setKiroInternalThinkingMirrorHopHeader(hdr)
}

// publishKiroInternalThinkingSideChannel stashes upstream thinking + signature for QA and,
// only on prod→edge mirror hops, emits the wire side channel (SSE comment or
// response header) that prod passthrough reads and strips before the end client.
func publishKiroInternalThinkingSideChannel(c *gin.Context, w io.Writer, hdr http.Header, thinking, signature string) {
	stashKiroInternalThinkingBlocks(c, thinking, signature)
	if !kiroInternalThinkingMirrorHopRequested(c) {
		return
	}
	if w != nil {
		_ = writeKiroInternalThinkingSSEComment(w, thinking, signature)
	}
	if hdr != nil {
		writeKiroInternalThinkingResponseHeader(hdr, thinking, signature)
	}
}

// kiroInternalThinkingBlockJSON returns one Anthropic-shaped thinking block JSON
// string for QA/traj export. Signature is included only when Kiro upstream sent it.
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
	c.Set(kiroInternalThinkingGinKey, blocks)
}

func writeKiroInternalThinkingResponseHeader(hdr http.Header, thinking, signature string) {
	if hdr == nil {
		return
	}
	payload := encodeKiroInternalThinkingPayload(kiroInternalThinkingBlocksFromCapture(thinking, signature))
	if payload == "" {
		return
	}
	hdr.Set(kiroInternalThinkingResponseHeader, payload)
}

func writeKiroInternalThinkingSSEComment(w io.Writer, thinking, signature string) error {
	payload := encodeKiroInternalThinkingPayload(kiroInternalThinkingBlocksFromCapture(thinking, signature))
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

package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

const maxCompactedChatBody = 8 << 20

type compactorState int

const (
	compactorStateNormal compactorState = iota
	compactorStateInStringPrefix
	compactorStateInStringNormal
	compactorStateDataURIMetadata
	compactorStateDataURIPayload
)

type streamCompactor struct {
	state     compactorState
	prefixBuf [32]byte
	prefixLen int
	inEscape  bool
	hasher    hash.Hash
	metaCount int
}

func (c *streamCompactor) Write(p []byte, out *bytes.Buffer) {
	for i := 0; i < len(p); i++ {
		b := p[i]
		switch c.state {
		case compactorStateNormal:
			_ = out.WriteByte(b)
			if b == '"' {
				c.state = compactorStateInStringPrefix
				c.prefixLen = 0
				c.inEscape = false
			}

		case compactorStateInStringPrefix:
			if c.prefixLen == 0 && (b == ' ' || b == '\t' || b == '\r' || b == '\n') {
				_ = out.WriteByte(b)
				continue
			}

			target := "data:image/"
			expectedChar := target[c.prefixLen]
			matched := b == expectedChar || (b >= 'A' && b <= 'Z' && b+32 == expectedChar) || (b >= 'a' && b <= 'z' && b-32 == expectedChar)

			if matched {
				c.prefixBuf[c.prefixLen] = b
				c.prefixLen++
				if c.prefixLen == len(target) {
					_, _ = out.Write(c.prefixBuf[:c.prefixLen])
					c.prefixLen = 0
					c.state = compactorStateDataURIMetadata
					c.metaCount = 0
				}
			} else {
				if c.prefixLen > 0 {
					_, _ = out.Write(c.prefixBuf[:c.prefixLen])
					c.prefixLen = 0
				}
				_ = out.WriteByte(b)
				switch b {
				case '\\':
					c.inEscape = true
					c.state = compactorStateInStringNormal
				case '"':
					c.state = compactorStateNormal
				default:
					c.state = compactorStateInStringNormal
				}
			}

		case compactorStateInStringNormal:
			_ = out.WriteByte(b)
			if c.inEscape {
				c.inEscape = false
			} else if b == '\\' {
				c.inEscape = true
			} else if b == '"' {
				c.state = compactorStateNormal
			}

		case compactorStateDataURIMetadata:
			_ = out.WriteByte(b)
			c.metaCount++
			if c.inEscape {
				c.inEscape = false
			} else if b == '\\' {
				c.inEscape = true
			} else if b == '"' {
				c.state = compactorStateNormal
			} else if b == ',' || c.metaCount > 128 {
				if c.hasher == nil {
					c.hasher = sha256.New()
				} else {
					c.hasher.Reset()
				}
				c.state = compactorStateDataURIPayload
			}

		case compactorStateDataURIPayload:
			if c.inEscape {
				_, _ = c.hasher.Write([]byte{b})
				c.inEscape = false
			} else if b == '\\' {
				_, _ = c.hasher.Write([]byte{b})
				c.inEscape = true
			} else if b == '"' {
				sum := c.hasher.Sum(nil)
				_, _ = out.WriteString(hex.EncodeToString(sum))
				_ = out.WriteByte('"')
				c.state = compactorStateNormal
			} else {
				nextSpecial := bytes.IndexAny(p[i:], "\"\\")
				if nextSpecial == -1 {
					_, _ = c.hasher.Write(p[i:])
					i = len(p) - 1
				} else if nextSpecial > 0 {
					_, _ = c.hasher.Write(p[i : i+nextSpecial])
					i += nextSpecial - 1
				} else {
					_, _ = c.hasher.Write([]byte{b})
				}
			}
		}
	}
}

func (c *streamCompactor) Flush(out *bytes.Buffer) {
	if c.prefixLen > 0 {
		_, _ = out.Write(c.prefixBuf[:c.prefixLen])
		c.prefixLen = 0
	}
	if c.state == compactorStateDataURIPayload && c.hasher != nil {
		sum := c.hasher.Sum(nil)
		_, _ = out.WriteString(hex.EncodeToString(sum))
		_ = out.WriteByte('"')
		c.state = compactorStateNormal
	}
}

type chatImageObserver struct {
	gin.ResponseWriter
	mu        sync.Mutex
	status    int
	detected  bool
	isSSE     bool
	pending   []byte
	compactor streamCompactor
	jsonBuf   bytes.Buffer
	lineBuf   bytes.Buffer
	seenSSE   map[[32]byte]struct{}
}

func (w *chatImageObserver) WriteHeader(code int) {
	w.mu.Lock()
	if w.status == 0 {
		w.status = code
	}
	w.mu.Unlock()
	w.ResponseWriter.WriteHeader(code)
}

func (w *chatImageObserver) Write(p []byte) (int, error) {
	original := p
	w.mu.Lock()
	w.observe(p)
	w.mu.Unlock()
	return w.ResponseWriter.Write(original)
}

func (w *chatImageObserver) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *chatImageObserver) Unwrap() http.ResponseWriter       { return w.ResponseWriter }

func (w *chatImageObserver) detectMode() bool {
	if w.ResponseWriter != nil && w.Header() != nil {
		ct := w.Header().Get("Content-Type")
		if strings.Contains(ct, "text/event-stream") {
			w.isSSE = true
			w.detected = true
			return true
		}
		if strings.Contains(ct, "application/json") {
			w.isSSE = false
			w.detected = true
			return true
		}
	}
	trimmed := bytes.TrimLeft(w.pending, " \t\r\n")
	if len(trimmed) == 0 {
		return false
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte(":")) || bytes.HasPrefix(trimmed, []byte("id:")) || bytes.HasPrefix(trimmed, []byte("retry:")) {
		w.isSSE = true
		w.detected = true
		return true
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		w.isSSE = false
		w.detected = true
		return true
	}
	return false
}

func (w *chatImageObserver) observe(p []byte) {
	if len(p) == 0 {
		return
	}
	if !w.detected {
		w.pending = append(w.pending, p...)
		if !w.detectMode() {
			if len(w.pending) < 64 {
				return
			}
			w.detected = true
			w.isSSE = false
		}
		pending := w.pending
		w.pending = nil
		w.observeDetected(pending)
		return
	}
	w.observeDetected(p)
}

func (w *chatImageObserver) observeDetected(p []byte) {
	if w.isSSE {
		if w.seenSSE == nil {
			w.seenSSE = make(map[[32]byte]struct{})
		}
		w.compactor.Write(p, &w.lineBuf)
		for {
			b := w.lineBuf.Bytes()
			idx := bytes.IndexByte(b, '\n')
			if idx == -1 {
				break
			}
			line := make([]byte, idx)
			copy(line, b[:idx])
			remainderLen := len(b) - (idx + 1)
			copy(b, b[idx+1:])
			w.lineBuf.Truncate(remainderLen)
			w.processSSELine(line)
		}
		return
	}
	if w.jsonBuf.Len() < maxCompactedChatBody {
		w.compactor.Write(p, &w.jsonBuf)
	}
}

func (w *chatImageObserver) processSSELine(line []byte) {
	line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
		return
	}
	var v any
	if json.Unmarshal(line, &v) == nil {
		walkImageOutputs(v, func(s string) {
			h := sha256.Sum256([]byte(s))
			w.seenSSE[h] = struct{}{}
		})
	}
}

func (w *chatImageObserver) imageCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	status := w.status
	if status == 0 && w.ResponseWriter != nil {
		status = w.Status()
	}
	if status >= 400 {
		return 0
	}
	if !w.detected {
		_ = w.detectMode()
		if len(w.pending) > 0 {
			pending := w.pending
			w.pending = nil
			w.observeDetected(pending)
		}
	}
	if w.isSSE {
		if w.lineBuf.Len() > 0 {
			w.compactor.Flush(&w.lineBuf)
			w.processSSELine(w.lineBuf.Bytes())
			w.lineBuf.Reset()
		}
		return len(w.seenSSE)
	}
	w.compactor.Flush(&w.jsonBuf)
	return countChatImageOutputs(w.jsonBuf.Bytes())
}

func walkImageOutputs(v any, onImage func(s string)) {
	var walk func(any)
	walk = func(node any) {
		switch x := node.(type) {
		case map[string]any:
			for _, val := range x {
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		case string:
			s := strings.TrimSpace(x)
			if strings.HasPrefix(strings.ToLower(s), "data:image/") {
				onImage(s)
			}
		}
	}
	walk(v)
}

func countChatImageOutputs(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	seen := map[[32]byte]struct{}{}
	jsonCount := 0
	var v any
	if json.Unmarshal(body, &v) == nil {
		walkImageOutputs(v, func(s string) {
			jsonCount++
		})
		return jsonCount
	}
	// SSE may contain multiple JSON envelopes; dedupe identical data URIs.
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		if json.Unmarshal(line, &v) == nil {
			walkImageOutputs(v, func(s string) {
				h := sha256.Sum256([]byte(s))
				seen[h] = struct{}{}
			})
		}
	}
	return len(seen)
}

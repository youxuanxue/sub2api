package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

const maxObservedChatBody = 8 << 20

type chatImageObserver struct {
	gin.ResponseWriter
	mu     sync.Mutex
	buf    bytes.Buffer
	status int
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
	if w.buf.Len() < maxObservedChatBody {
		n := maxObservedChatBody - w.buf.Len()
		if len(p) > n {
			p = p[:n]
		}
		_, _ = w.buf.Write(p)
	}
	w.mu.Unlock()
	return w.ResponseWriter.Write(original)
}
func (w *chatImageObserver) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *chatImageObserver) Unwrap() http.ResponseWriter       { return w.ResponseWriter }
func (w *chatImageObserver) imageCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status >= 400 {
		return 0
	}
	return countChatImageOutputs(w.buf.Bytes())
}

func countChatImageOutputs(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	seen := map[[32]byte]struct{}{}
	jsonCount := 0
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
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
				jsonCount++
				h := sha256.Sum256([]byte(s))
				seen[h] = struct{}{}
			}
		}
	}
	var v any
	if json.Unmarshal(body, &v) == nil {
		walk(v)
		return jsonCount
	}
	// SSE may contain multiple JSON envelopes; dedupe identical data URIs.
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		if json.Unmarshal(line, &v) == nil {
			walk(v)
		}
	}
	return len(seen)
}

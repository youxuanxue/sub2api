package service

import (
	"bytes"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The adaptor writes before returning canonical billable usage. Keep only the
// JSON response or terminal SSE usage tail until that result is available;
// content chunks still reach the client immediately. Oversized tails fail open.
const maxBridgeUsageCostBuffer = 8 << 20

type bridgeUsageCostWriter struct {
	gin.ResponseWriter
	mu       sync.Mutex
	stream   bool
	detected bool
	holding  bool
	passthru bool
	finished bool
	pending  []byte
}

func (w *bridgeUsageCostWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *bridgeUsageCostWriter) WriteHeader(code int) {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(code)
}
func (w *bridgeUsageCostWriter) WriteHeaderNow() {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeaderNow()
}
func (w *bridgeUsageCostWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *bridgeUsageCostWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.passthru || w.finished {
		return w.ResponseWriter.Write(p)
	}
	if !w.detected {
		// Adaptors can force SSE for thinking models even when the client
		// requested JSON. Follow their actual wire format, like the attempt owner.
		if contentType := w.Header().Get("Content-Type"); contentType != "" {
			w.stream = strings.HasPrefix(contentType, "text/event-stream")
		}
		w.detected = true
	}
	// The injected field changes the encoded body length.
	w.Header().Del("Content-Length")
	if len(w.pending)+len(p) > maxBridgeUsageCostBuffer {
		w.passthru = true
		if err := w.drain(); err != nil {
			return 0, err
		}
		return w.ResponseWriter.Write(p)
	}
	w.pending = append(w.pending, p...)
	if !w.stream || w.holding {
		return len(p), nil
	}
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		line := w.pending[:end+1]
		if bridgeTerminalUsageLine(line) {
			// A terminal chunk may also contain the only answer/tool delta.
			// Release its choices now and retain a usage-only copy; otherwise
			// the candidate first-output timer could expire during settlement.
			payload, _ := extractOpenAISSEDataLine(strings.TrimRight(string(line), "\r\n"))
			if len(gjson.Get(payload, "choices").Array()) > 0 {
				choices, choiceErr := sjson.Delete(payload, "usage")
				usage, usageErr := sjson.SetRaw(payload, "choices", "[]")
				if choiceErr == nil && usageErr == nil {
					if _, err := w.ResponseWriter.Write([]byte("data: " + choices + "\n\n")); err != nil {
						return 0, err
					}
					w.pending = append([]byte("data: "+usage+"\n"), w.pending[end+1:]...)
				}
			}
			w.holding = true
			break
		}
		if _, err := w.ResponseWriter.Write(line); err != nil {
			return 0, err
		}
		w.pending = w.pending[end+1:]
	}
	return len(p), nil
}

func bridgeTerminalUsageLine(line []byte) bool {
	payload, ok := extractOpenAISSEDataLine(strings.TrimRight(string(line), "\r\n"))
	if !ok || !gjson.Valid(payload) || !gjson.Get(payload, "usage").IsObject() {
		return false
	}
	choices := gjson.Get(payload, "choices").Array()
	if len(choices) == 0 {
		return true
	}
	for _, choice := range choices {
		if choice.Get("finish_reason").String() != "" {
			return true
		}
	}
	return false
}

func (w *bridgeUsageCostWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Never flush pending usage before canonical settlement has completed.
	w.Header().Del("Content-Length")
	w.ResponseWriter.Flush()
}

func (w *bridgeUsageCostWriter) drain() error {
	if len(w.pending) == 0 {
		return nil
	}
	pending := w.pending
	w.pending = nil
	_, err := w.ResponseWriter.Write(pending)
	return err
}

// finish invokes settlement only for a complete existing usage object. The
// returned breakdown is the same one that RecordUsage must reuse. On errors,
// callers pass nil and the original bytes are released without invented usage.
func (w *bridgeUsageCostWriter) finish(settle func() *CostBreakdown) (*CostBreakdown, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished {
		return nil, nil
	}
	w.finished = true
	if len(w.pending) == 0 {
		return nil, nil
	}
	var cost *CostBreakdown
	if settle != nil && !w.passthru && w.Status() < http.StatusBadRequest {
		if w.stream {
			lines := bytes.SplitAfter(w.pending, []byte("\n"))
			// Some adaptors emit more than one terminal usage frame. Expose this
			// request's cost once, on the last one, so summing chunks is safe.
			for i := len(lines) - 1; i >= 0; i-- {
				if !bridgeTerminalUsageLine(lines[i]) {
					continue
				}
				cost = settle()
				if cost != nil {
					for j := 0; j < i; j++ {
						if !bridgeTerminalUsageLine(lines[j]) {
							continue
						}
						line := strings.TrimRight(string(lines[j]), "\r\n")
						payload, _ := extractOpenAISSEDataLine(line)
						clean, err := sjson.Delete(payload, "usage.cost")
						if err == nil && clean != payload {
							lines[j] = []byte("data: " + clean + string(lines[j])[len(line):])
						}
					}
					line := strings.TrimRight(string(lines[i]), "\r\n")
					suffix := string(lines[i])[len(line):]
					lines[i] = []byte(InjectUsageCostSSEDataLine(line, cost.TotalCost) + suffix)
					w.pending = bytes.Join(lines, nil)
				}
				break
			}
		} else if gjson.ValidBytes(w.pending) && gjson.GetBytes(w.pending, "usage").IsObject() {
			cost = settle()
			if cost != nil {
				w.pending = InjectUsageCostJSON(w.pending, cost.TotalCost)
			}
		}
	}
	w.Header().Del("Content-Length")
	err := w.drain()
	if w.stream {
		w.ResponseWriter.Flush()
	}
	return cost, err
}

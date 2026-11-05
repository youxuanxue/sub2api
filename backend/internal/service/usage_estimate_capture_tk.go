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

// Usage-estimate capture writer: the delivered-output source and the
// client-usage writer for the zero-usage estimate owner
// (usage_estimate_fallback_tk.go). It is installed innermost on every
// text-inference route by WithUsageEstimateCapture, so ops and QA
// capture see the same bytes as the client.
//
// Non-stream JSON bodies are held until the handler returns, so an estimate
// can be written into the usage object. Streams pass through immediately; only
// the terminal usage frames are held (chat usage-only chunk/[DONE], Responses
// response.completed, Messages message_delta). Gemini streams are never held:
// their usageMetadata rides on content chunks, so holding would delay output.
// Oversized bodies pass through and are not estimated.

const (
	usageEstimateCaptureContextKey = "tk_usage_estimate_capture"
	usageEstimateCaptureMaxBytes   = 8 << 20
)

const (
	usageEstimateProtocolChat      = "openai_chat"
	usageEstimateProtocolResponses = "openai_responses"
	usageEstimateProtocolMessages  = "anthropic_messages"
	usageEstimateProtocolGemini    = "gemini_generate_content"
)

// usageEstimateProtocolForPath maps an inbound path to the text protocol the
// client speaks. Paths outside the text set (count tokens, embeddings, input
// token counting) return "" and are never estimated.
func usageEstimateProtocolForPath(path string) (protocol string, geminiStream bool) {
	p := strings.TrimRight(strings.TrimSpace(path), "/")
	switch {
	// Compact streams its own keepalive before the JSON body; holding would
	// defeat it, and compact always reports usage.
	case strings.Contains(p, "count_tokens") || strings.Contains(p, "input_tokens") || strings.HasSuffix(p, "/compact"):
		return "", false
	case strings.HasSuffix(p, "/chat/completions"):
		return usageEstimateProtocolChat, false
	case strings.HasSuffix(p, "/messages"):
		return usageEstimateProtocolMessages, false
	case strings.HasSuffix(p, ":streamGenerateContent") || strings.HasSuffix(p, "/streamGenerateContent"):
		return usageEstimateProtocolGemini, true
	case strings.HasSuffix(p, ":generateContent") || strings.HasSuffix(p, "/generateContent"):
		return usageEstimateProtocolGemini, false
	case strings.Contains(p, "/responses"):
		return usageEstimateProtocolResponses, false
	}
	return "", false
}

// WithUsageEstimateCapture wraps a route middleware (the inbound endpoint
// normalizer, which every gateway chain runs after ops/QA capture) so the
// capture writer is installed innermost. Routes outside the text protocol set
// run next unchanged.
func WithUsageEstimateCapture(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil || c.Request.URL == nil || c.Writer == nil || c.Request.Method != http.MethodPost {
			next(c)
			return
		}
		protocol, geminiStream := usageEstimateProtocolForPath(c.Request.URL.Path)
		if protocol == "" {
			next(c)
			return
		}
		original := c.Writer
		w := &usageEstimateCaptureWriter{ResponseWriter: original, protocol: protocol, geminiStream: geminiStream}
		c.Set(usageEstimateCaptureContextKey, w)
		c.Writer = w
		defer func() {
			_ = w.release()
			if c.Writer == w {
				c.Writer = original
			}
		}()
		next(c)
	}
}

func usageEstimateCaptureFromContext(c *gin.Context) *usageEstimateCaptureWriter {
	if c == nil {
		return nil
	}
	v, _ := c.Get(usageEstimateCaptureContextKey)
	w, _ := v.(*usageEstimateCaptureWriter)
	return w
}

type usageEstimateCaptureWriter struct {
	gin.ResponseWriter
	mu           sync.Mutex
	protocol     string
	geminiStream bool

	detected bool
	stream   bool
	holdBody bool // non-stream body held for patching
	noPatch  bool // headers committed with a fixed length; bill only
	released bool
	oversize bool
	errored  bool

	pending  []byte // held bytes (whole JSON body, or SSE tail from the terminal frame)
	partial  []byte // incomplete SSE line awaiting its newline
	holding  bool   // SSE terminal frame reached; keep the tail
	tee      []byte // passthrough JSON copy for output extraction (Gemini)
	teeBytes int

	deltas    strings.Builder
	completed strings.Builder
	chunkID   string
	chunkMdl  string
	created   int64

	estimate *usageEstimateTokens
	cost     *float64 // client usage.cost of the priced estimate, when settled
}

func (w *usageEstimateCaptureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *usageEstimateCaptureWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *usageEstimateCaptureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.released {
		return w.ResponseWriter.Write(p)
	}
	if !w.detected {
		w.detect()
	}
	if w.Status() >= http.StatusBadRequest {
		w.errored = true
	}
	if w.stream {
		return w.writeStream(p)
	}
	if w.holdBody {
		if len(w.pending)+len(p) > usageEstimateCaptureMaxBytes {
			w.oversize, w.holdBody = true, false
			if err := w.drainPending(); err != nil {
				return 0, err
			}
			return w.ResponseWriter.Write(p)
		}
		w.pending = append(w.pending, p...)
		return len(p), nil
	}
	w.teeAppend(p)
	return w.ResponseWriter.Write(p)
}

func (w *usageEstimateCaptureWriter) detect() {
	w.detected = true
	w.stream = strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream")
	if w.stream || w.Status() >= http.StatusBadRequest {
		return
	}
	if w.protocol == usageEstimateProtocolGemini && w.geminiStream {
		return // JSON-array streaming: pass through, tee only
	}
	if w.ResponseWriter.Written() && w.Header().Get("Content-Length") != "" {
		w.noPatch = true
		return
	}
	w.Header().Del("Content-Length")
	w.holdBody = true
}

func (w *usageEstimateCaptureWriter) teeAppend(p []byte) {
	if w.oversize {
		return
	}
	if w.teeBytes+len(p) > usageEstimateCaptureMaxBytes {
		w.oversize, w.tee = true, nil
		return
	}
	w.tee = append(w.tee, p...)
	w.teeBytes += len(p)
}

// writeStream forwards complete SSE lines, observing each one, and starts
// holding at the protocol's terminal usage frame.
func (w *usageEstimateCaptureWriter) writeStream(p []byte) (int, error) {
	if w.holding || w.oversize {
		if !w.oversize {
			w.observeLines(p)
			if len(w.pending)+len(p) <= usageEstimateCaptureMaxBytes {
				w.pending = append(w.pending, p...)
				return len(p), nil
			}
			w.oversize = true
			if err := w.drainPending(); err != nil {
				return 0, err
			}
		}
		return w.ResponseWriter.Write(p)
	}
	buf := append(w.partial, p...)
	w.partial = nil
	written := 0
	for {
		end := bytes.IndexByte(buf[written:], '\n')
		if end < 0 {
			break
		}
		line := buf[written : written+end+1]
		w.observeLine(line)
		if w.startsHold(line) {
			w.holding = true
			w.pending = append(w.pending, buf[written:]...)
			return len(p), nil
		}
		if _, err := w.ResponseWriter.Write(line); err != nil {
			return 0, err
		}
		written += end + 1
	}
	if written < len(buf) {
		if len(buf)-written > usageEstimateCaptureMaxBytes {
			w.oversize = true
			if _, err := w.ResponseWriter.Write(buf[written:]); err != nil {
				return 0, err
			}
		} else {
			w.partial = append([]byte(nil), buf[written:]...)
		}
	}
	return len(p), nil
}

func (w *usageEstimateCaptureWriter) observeLines(p []byte) {
	for _, line := range bytes.SplitAfter(p, []byte("\n")) {
		w.observeLine(line)
	}
}

func (w *usageEstimateCaptureWriter) observeLine(line []byte) {
	text := strings.TrimRight(string(line), "\r\n")
	if strings.HasPrefix(text, "event:") && strings.TrimSpace(strings.TrimPrefix(text, "event:")) == "error" {
		w.errored = true
		return
	}
	payload, ok := extractOpenAISSEDataLine(text)
	if !ok {
		return
	}
	payload = strings.TrimSpace(payload)
	if payload == "" || payload == "[DONE]" || !gjson.Valid(payload) {
		return
	}
	root := gjson.Parse(payload)
	switch root.Get("type").String() {
	case "error", "response.failed":
		w.errored = true
		return
	case "response.completed", "response.incomplete", "response.done":
		if w.deltas.Len()+w.completed.Len() < usageEstimateCaptureMaxBytes {
			_, _ = w.completed.WriteString(usageEstimateResponsesOutputText(root.Get("response")))
		}
		return
	}
	if root.Get("error").IsObject() && !root.Get("choices").Exists() && !root.Get("candidates").Exists() {
		w.errored = true
		return
	}
	if w.chunkID == "" {
		w.chunkID = root.Get("id").String()
	}
	if w.chunkMdl == "" {
		w.chunkMdl = root.Get("model").String()
	}
	if w.created == 0 {
		w.created = root.Get("created").Int()
	}
	if w.deltas.Len()+w.completed.Len() < usageEstimateCaptureMaxBytes {
		_, _ = w.deltas.WriteString(usageEstimatePayloadOutputText(root, true))
	} else {
		w.oversize = true
	}
}

// startsHold reports the first frame after which only terminal usage
// metadata follows for the client protocol.
func (w *usageEstimateCaptureWriter) startsHold(line []byte) bool {
	payload, ok := extractOpenAISSEDataLine(strings.TrimRight(string(line), "\r\n"))
	if !ok {
		return false
	}
	payload = strings.TrimSpace(payload)
	switch w.protocol {
	case usageEstimateProtocolChat:
		if payload == "[DONE]" {
			return true
		}
		if !gjson.Valid(payload) {
			return false
		}
		// Hold only from a usage-only chunk or [DONE]. Some converters attach
		// usage to every content chunk; holding those would buffer the stream.
		return isOpenAIChatUsageOnlyStreamChunk(payload)
	case usageEstimateProtocolResponses:
		switch gjson.Get(payload, "type").String() {
		case "response.completed", "response.incomplete", "response.done":
			return true
		}
	case usageEstimateProtocolMessages:
		return gjson.Get(payload, "type").String() == "message_delta"
	}
	return false
}

// Flush on a held JSON body means the writer is actually streaming without an
// event-stream Content-Type. Stop holding so output is never delayed; an
// estimate is then billed but not echoed (noPatch).
func (w *usageEstimateCaptureWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.holdBody && !w.released {
		w.holdBody, w.noPatch = false, true
		w.teeAppend(w.pending)
		_ = w.drainPending()
	}
	w.ResponseWriter.Flush()
}

// Size and Written report every accepted byte, so failover guards that
// compare sizes before and after a forward keep their meaning.
func (w *usageEstimateCaptureWriter) Size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	held := len(w.pending) + len(w.partial)
	size := w.ResponseWriter.Size()
	if held == 0 {
		return size
	}
	if size < 0 {
		size = 0
	}
	return size + held
}

func (w *usageEstimateCaptureWriter) Written() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseWriter.Written() || len(w.pending) > 0 || len(w.partial) > 0
}

func (w *usageEstimateCaptureWriter) drainPending() error {
	pending := append(w.pending, w.partial...)
	w.pending, w.partial = nil, nil
	if len(pending) == 0 {
		return nil
	}
	_, err := w.ResponseWriter.Write(pending)
	return err
}

// deliveredSuccess reports a successful, complete, non-error delivery.
func (w *usageEstimateCaptureWriter) deliveredSuccess() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.released && !w.oversize && !w.errored && w.Status() < http.StatusBadRequest
}

func (w *usageEstimateCaptureWriter) deliveredOutputText() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stream {
		if strings.TrimSpace(w.deltas.String()) != "" {
			return w.deltas.String()
		}
		return w.completed.String()
	}
	if w.holdBody {
		return usageEstimateDeliveredOutputText(w.pending)
	}
	return usageEstimateDeliveredOutputText(w.tee)
}

func (w *usageEstimateCaptureWriter) applyEstimate(est usageEstimateTokens, cost *float64) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.estimate, w.cost = &est, cost
}

// release writes held bytes, with the estimate patched into the client usage
// when one was applied. It runs once, when the handler returns.
func (w *usageEstimateCaptureWriter) release() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.released {
		return nil
	}
	w.released = true
	if w.estimate != nil && !w.noPatch {
		if w.stream {
			w.pending = w.patchStreamTail(w.pending, *w.estimate, w.cost)
		} else if w.holdBody {
			w.pending = usageEstimatePatchJSON(w.protocol, w.pending, *w.estimate, w.cost)
		}
	}
	err := w.drainPending()
	if w.stream {
		w.ResponseWriter.Flush()
	}
	return err
}

func (w *usageEstimateCaptureWriter) patchStreamTail(tail []byte, est usageEstimateTokens, cost *float64) []byte {
	lines := bytes.SplitAfter(tail, []byte("\n"))
	target, doneIdx := -1, -1
	for i, raw := range lines {
		line := strings.TrimRight(string(raw), "\r\n")
		payload, ok := extractOpenAISSEDataLine(line)
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			if doneIdx < 0 {
				doneIdx = i
			}
			continue
		}
		switch w.protocol {
		case usageEstimateProtocolChat:
			if isOpenAIChatUsageOnlyStreamChunk(payload) {
				target = i
			}
		case usageEstimateProtocolResponses:
			switch gjson.Get(payload, "type").String() {
			case "response.completed", "response.incomplete", "response.done":
				target = i
			}
		case usageEstimateProtocolMessages:
			if gjson.Get(payload, "type").String() == "message_delta" {
				target = i
			}
		}
	}
	if target >= 0 {
		line := strings.TrimRight(string(lines[target]), "\r\n")
		suffix := string(lines[target])[len(line):]
		payload, _ := extractOpenAISSEDataLine(line)
		patched := usageEstimatePatchJSON(w.protocol, []byte(strings.TrimSpace(payload)), est, cost)
		lines[target] = []byte("data: " + string(patched) + suffix)
		return bytes.Join(lines, nil)
	}
	if w.protocol != usageEstimateProtocolChat {
		return tail
	}
	// Chat stream without a usage chunk: add the include_usage shape before
	// [DONE] (or at the end when the stream had no [DONE]).
	chunk := []byte(`{"object":"chat.completion.chunk","choices":[]}`)
	chunk, _ = sjson.SetBytes(chunk, "id", w.chunkID)
	chunk, _ = sjson.SetBytes(chunk, "created", w.created)
	chunk, _ = sjson.SetBytes(chunk, "model", w.chunkMdl)
	frame := []byte("data: " + string(usageEstimatePatchJSON(w.protocol, chunk, est, cost)) + "\n\n")
	if doneIdx < 0 {
		return append(tail, frame...)
	}
	out := make([]byte, 0, len(tail)+len(frame))
	for i, raw := range lines {
		if i == doneIdx {
			out = append(out, frame...)
		}
		out = append(out, raw...)
	}
	return out
}

// usageEstimatePatchJSON replaces the usage object with the billed estimate
// in the client protocol's shape. Detail fields are dropped (the estimate has
// no cache/reasoning split). usage.cost carries the same settlement that
// RecordUsage reuses, so client cost equals billed cost; Gemini has no cost
// field.
func usageEstimatePatchJSON(protocol string, body []byte, est usageEstimateTokens, cost *float64) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	var path string
	var usage map[string]any
	switch protocol {
	case usageEstimateProtocolChat:
		path = "usage"
		usage = map[string]any{"prompt_tokens": est.Input, "completion_tokens": est.Output, "total_tokens": est.Input + est.Output}
	case usageEstimateProtocolResponses:
		path = "usage"
		if gjson.GetBytes(body, "response").IsObject() {
			path = "response.usage"
		}
		usage = map[string]any{"input_tokens": est.Input, "output_tokens": est.Output, "total_tokens": est.Input + est.Output}
	case usageEstimateProtocolMessages:
		path = "usage"
		usage = map[string]any{"input_tokens": est.Input, "output_tokens": est.Output}
	case usageEstimateProtocolGemini:
		path = "usageMetadata"
		usage = map[string]any{"promptTokenCount": est.Input, "candidatesTokenCount": est.Output, "totalTokenCount": est.Input + est.Output}
	default:
		return body
	}
	if cost != nil && protocol != usageEstimateProtocolGemini {
		usage["cost"] = *cost
	}
	next, err := sjson.SetBytes(body, path, usage)
	if err != nil {
		return body
	}
	return next
}

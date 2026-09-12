package replaycapture

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ResponseObserver validates terminal/error semantics without retaining an
// unbounded response. In particular it sees errors beyond the ordinary QA cap.
type ResponseObserver struct {
	AllowText   bool
	contentType string
	pending     []byte
	eventData   []byte
	eventType   string
	sawData     bool
	terminal    bool
	failed      bool
	bytes       int64
}

func (o *ResponseObserver) Write(contentType string, p []byte) {
	o.contentType = strings.ToLower(strings.Split(contentType, ";")[0])
	o.bytes += int64(len(p))
	if o.failed {
		return
	}
	if !strings.Contains(o.contentType, "text/event-stream") {
		if len(o.pending)+len(p) > MaxBodyBytes {
			o.failed = true
			return
		}
		o.pending = append(o.pending, p...)
		return
	}
	// Process lines incrementally; an individual oversized frame is ineligible.
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		n := len(p)
		if i >= 0 {
			n = i + 1
		}
		if len(o.pending)+n > MaxBodyBytes {
			o.failed = true
			return
		}
		o.pending = append(o.pending, p[:n]...)
		p = p[n:]
		if i < 0 {
			return
		}
		line := strings.TrimSuffix(strings.TrimSuffix(string(o.pending), "\n"), "\r")
		o.pending = o.pending[:0]
		if line == "" {
			if o.eventType == "error" {
				o.failed = true
			}
			if o.sawData {
				data := bytes.TrimSpace(o.eventData)
				if string(data) == "[DONE]" {
					o.terminal = true
				} else if len(data) > 0 {
					var event map[string]any
					if json.Unmarshal(data, &event) != nil {
						o.failed = true
					} else {
						o.event(event)
					}
				}
			}
			o.eventData = o.eventData[:0]
			o.eventType = ""
			o.sawData = false
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			o.eventType = value
		case "data":
			if len(o.eventData)+len(value)+1 > MaxBodyBytes {
				o.failed = true
				return
			}
			o.sawData = true
			o.eventData = append(o.eventData, []byte(value)...)
			o.eventData = append(o.eventData, '\n')
		}
	}
}
func (o *ResponseObserver) event(e map[string]any) {
	if e["error"] != nil || e["status"] == "failed" || e["status"] == "incomplete" {
		o.failed = true
	}
	switch e["type"] {
	case "error", "response.failed", "response.incomplete":
		o.failed = true
	case "message_stop", "response.completed":
		o.terminal = true
	}
	if r, ok := e["response"].(map[string]any); ok && (r["error"] != nil || r["status"] == "failed" || r["status"] == "incomplete") {
		o.failed = true
	}
	for _, key := range []string{"choices", "candidates"} {
		if entries, ok := e[key].([]any); ok {
			for _, v := range entries {
				if c, ok := v.(map[string]any); ok {
					if s, ok := c["finish_reason"].(string); ok && s != "" {
						o.terminal = true
					}
					if s, ok := c["finishReason"].(string); ok && s != "" {
						o.terminal = true
					}
				}
			}
		}
	}
}
func (o *ResponseObserver) Successful(status int) bool {
	if status < 200 || status >= 300 || o.failed || o.bytes == 0 {
		return false
	}
	if strings.Contains(o.contentType, "text/event-stream") {
		return o.terminal && len(bytes.TrimSpace(o.pending)) == 0 && !o.sawData && o.eventType == ""
	}
	if strings.Contains(o.contentType, "json") {
		var data any
		if json.Unmarshal(o.pending, &data) != nil {
			return false
		}
		switch v := data.(type) {
		case map[string]any:
			o.event(v)
			return len(v) > 0 && !o.failed
		case []any:
			return len(v) > 0
		default:
			return false
		}
	}
	if o.AllowText && (o.contentType == "text/plain" || o.contentType == "text/vtt" || o.contentType == "application/x-subrip") {
		return len(bytes.TrimSpace(o.pending)) > 0
	}
	return strings.HasPrefix(o.contentType, "audio/") || strings.HasPrefix(o.contentType, "image/")
}

func (o *ResponseObserver) Fail() { o.failed = true }

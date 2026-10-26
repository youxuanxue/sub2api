package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

type imageForwardTimingKey struct{}

// One forward can contain several HTTP calls (model fallback or n>1). Handler
// retries get a new forward index; all records retain the inbound request ID.
type imageForwardTiming struct {
	mu       sync.Mutex
	started  time.Time
	attempts int
	log      *zap.Logger
}

func beginImageForwardTiming(ctx context.Context, c *gin.Context, account *Account) (context.Context, func(bool, int)) {
	index := c.GetInt("image_forward_index") + 1
	c.Set("image_forward_index", index)
	f := &imageForwardTiming{started: time.Now(),
		log: logger.FromContext(c.Request.Context()).With(zap.String("component", "audit.image_timing"), zap.Int64("account_id", account.ID), zap.Int("image_forward_index", index))}
	if userID, ok := c.Request.Context().Value(ctxkey.UserID).(int64); ok {
		f.log = f.log.With(zap.Int64("user_id", userID))
	}
	original := c.Writer
	writer := &imageTimingWriter{ResponseWriter: original}
	c.Writer = writer
	return context.WithValue(ctx, imageForwardTimingKey{}, f), func(failed bool, images int) {
		c.Writer = original
		f.mu.Lock()
		attempts := f.attempts
		f.mu.Unlock()
		f.log.Info("image.forward_timing", zap.Time("image_forward_started_at", f.started), zap.Int64("image_forward_ms", time.Since(f.started).Milliseconds()),
			zap.Int("image_attempt_count", attempts), zap.Bool("image_forward_failed", failed), zap.Int("image_count", images),
			zap.Int64("image_downstream_write_ms", writer.elapsed.Milliseconds()), zap.Int64("image_downstream_bytes", writer.bytes),
			zap.Bool("image_downstream_failed", writer.failed))
	}
}

// This wrapper is installed outside the JSON keepalive writer, after keepalive
// starts: its background heartbeats bypass this wrapper and are not image output.
// Embedded gin.ResponseWriter preserves status/size/flusher/hijack semantics.
type imageTimingWriter struct {
	gin.ResponseWriter
	elapsed time.Duration
	bytes   int64
	failed  bool
}

func (w *imageTimingWriter) Write(p []byte) (int, error) {
	start := time.Now()
	n, err := w.ResponseWriter.Write(p)
	w.elapsed += time.Since(start)
	w.bytes += int64(n)
	w.failed = w.failed || err != nil
	return n, err
}
func (w *imageTimingWriter) WriteString(p string) (int, error) {
	start := time.Now()
	n, err := w.ResponseWriter.WriteString(p)
	w.elapsed += time.Since(start)
	w.bytes += int64(n)
	w.failed = w.failed || err != nil
	return n, err
}
func (w *imageTimingWriter) Flush() {
	start := time.Now()
	w.ResponseWriter.Flush()
	w.elapsed += time.Since(start)
}

// All offsets are relative to this HTTP attempt. Traces are optional: custom
// transports/proxies may omit callbacks. Absent is not zero and GotConn includes
// pool wait, proxy setup and handshakes, not just TCP connect.
type imageUpstreamTiming struct {
	readTime time.Duration
	mu       sync.Mutex
	now      func() time.Time
	start    time.Time
	fields   map[string]int64
	reused   bool
	gotConn  bool
	status   int
	done     bool
	log      *zap.Logger
	parser   imageTimingSSE
}

func observeImageUpstream(req *http.Request) (*http.Request, *imageUpstreamTiming) {
	if req == nil {
		return nil, nil
	}
	f, _ := req.Context().Value(imageForwardTimingKey{}).(*imageForwardTiming)
	if f == nil {
		return req, nil
	}
	f.mu.Lock()
	f.attempts++
	index := f.attempts
	f.mu.Unlock()
	t := &imageUpstreamTiming{now: time.Now, start: time.Now(), fields: make(map[string]int64),
		log: f.log.With(zap.Int("image_attempt_index", index), zap.Int64("image_attempt_start_ms", time.Since(f.started).Milliseconds()))}
	t.fields["image_upstream_request_bytes"] = req.ContentLength
	trace := &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { t.mark("image_dns_start_ms") },
		DNSDone:      func(httptrace.DNSDoneInfo) { t.mark("image_dns_done_ms") },
		ConnectStart: func(string, string) { t.mark("image_connect_start_ms") },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				t.mark("image_connect_done_ms")
			}
		},
		TLSHandshakeStart: func() { t.mark("image_tls_start_ms") },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				t.mark("image_tls_done_ms")
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.done {
				return
			}
			t.fields["image_connection_count"]++
			t.markLocked("image_got_conn_ms")
			if !t.gotConn {
				t.reused = info.Reused
			}
			t.gotConn = true
		},
		WroteHeaders: func() { t.mark("image_request_headers_written_ms") },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				t.mu.Lock()
				if !t.done {
					t.fields["image_request_write_count"]++
					t.markLocked("image_request_written_ms")
				}
				t.mu.Unlock()
			}
		},
		GotFirstResponseByte: func() { t.mark("image_first_response_byte_ms") },
	}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace)), t
}
func (t *imageUpstreamTiming) mark(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.markLocked(key)
}
func (t *imageUpstreamTiming) markLocked(key string) {
	if _, exists := t.fields[key]; !exists && !t.done {
		t.fields[key] = t.now().Sub(t.start).Milliseconds()
	}
}

func (t *imageUpstreamTiming) response(resp *http.Response, err error) {
	if t == nil {
		return
	}
	if err != nil {
		t.finish("request_error")
		return
	}
	if resp == nil {
		t.finish("no_response")
		return
	}
	t.mu.Lock()
	t.status = resp.StatusCode
	t.markLocked("image_response_ready_ms")
	t.mu.Unlock()
	if resp.Body == nil {
		t.finish("no_body")
		return
	}
	resp.Body = &imageTimingBody{ReadCloser: resp.Body, timing: t, sse: isEventStreamResponse(resp.Header)}
}
func (t *imageUpstreamTiming) finish(reason string) {
	t.mu.Lock()
	if t.done {
		t.mu.Unlock()
		return
	}
	t.markLocked("image_body_end_ms")
	t.done = true
	fields := []zap.Field{zap.Int("upstream_status_code", t.status), zap.String("image_body_end_reason", reason)}
	for k, v := range t.fields {
		fields = append(fields, zap.Int64(k, v))
	}
	if t.gotConn {
		fields = append(fields, zap.Bool("image_connection_reused", t.reused))
	}
	if t.parser.truncated {
		fields = append(fields, zap.Bool("image_event_prefix_truncated", true))
	}
	t.mu.Unlock()
	t.log.Info("image.upstream_timing", fields...)
}

type imageTimingBody struct {
	io.ReadCloser
	timing *imageUpstreamTiming
	sse    bool
}

func (b *imageTimingBody) Read(p []byte) (int, error) {
	start := b.timing.now()
	n, err := b.ReadCloser.Read(p)
	t := b.timing
	t.mu.Lock()
	if !t.done {
		t.readTime += t.now().Sub(start)
		t.fields["image_body_read_ms"] = t.readTime.Milliseconds()
		t.fields["image_upstream_body_bytes"] += int64(n)
		if n > 0 {
			t.markLocked("image_first_body_byte_ms")
			t.fields["image_last_body_byte_ms"] = t.now().Sub(t.start).Milliseconds()
			if b.sse {
				t.parser.feed(p[:n], t.markLocked)
			}
		}
	}
	t.mu.Unlock()
	if errors.Is(err, io.EOF) {
		t.finish("eof")
	} else if err != nil {
		t.finish("read_error")
	}
	return n, err
}
func (b *imageTimingBody) Close() error {
	err := b.ReadCloser.Close()
	b.timing.finish("closed")
	return err
}

// Observe SSE at Read time, including buffered/non-streaming consumers. Keep
// only a bounded line prefix, never copy base64 images/full event bodies. Huge
// lines are scanned once for delimiters; unknown/late type fields remain absent.
// Event timestamps are taken at frame completion, not when the type prefix
// arrives: a large final image can take time to transfer after its event header.
const imageTimingPrefixLimit = 2048

type imageTimingSSE struct {
	prefix    []byte
	lineBytes int
	eventType string
	hasData   bool
	truncated bool
}

func (p *imageTimingSSE) feed(data []byte, mark func(string)) {
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		segment := data
		if end >= 0 {
			segment = data[:end]
		}
		p.lineBytes += len(segment)
		take := min(len(segment), imageTimingPrefixLimit-len(p.prefix))
		p.prefix = append(p.prefix, segment[:take]...)
		if take < len(segment) {
			p.truncated = true
		}
		if end < 0 {
			return
		}
		line := bytes.TrimSuffix(p.prefix, []byte{'\r'})
		if p.lineBytes == 0 || (p.lineBytes == 1 && len(line) == 0) {
			if p.hasData {
				mark("image_first_event_ms")
				if key := imageTimingEventKey(p.eventType); key != "" {
					mark(key)
				}
			}
			p.eventType = ""
			p.hasData = false
		} else if bytes.HasPrefix(line, []byte("event:")) {
			p.eventType = strings.TrimSpace(string(line[6:]))
		} else if bytes.HasPrefix(line, []byte("data:")) {
			p.hasData = true
			body := bytes.TrimSpace(line[5:])
			if typ := gjson.GetBytes(body, "type").String(); imageTimingEventKey(typ) != "" {
				p.eventType = typ
			} else if typ == "response.output_item.added" && gjson.GetBytes(body, "item.type").String() == "image_generation_call" {
				p.eventType = "response.image_generation_call.in_progress"
			} else if typ == "response.output_item.done" && gjson.GetBytes(body, "item.type").String() == "image_generation_call" {
				p.eventType = "image_result"
			}
		}
		p.prefix = p.prefix[:0]
		p.lineBytes = 0
		data = data[end+1:]
	}
}
func imageTimingEventKey(event string) string {
	switch event {
	case "response.created":
		return "image_response_created_ms"
	case "response.image_generation_call.in_progress":
		return "image_tool_started_ms"
	case "response.image_generation_call.generating":
		return "image_generating_ms"
	case "response.image_generation_call.partial_image", "image_generation.partial_image", "image_edit.partial_image":
		return "image_first_partial_ms"
	case "response.image_generation_call.completed":
		return "image_tool_completed_ms"
	case "image_result", "image_generation.completed", "image_edit.completed":
		return "image_result_ms"
	case "response.completed":
		return "image_response_completed_ms"
	case "response.failed", "response.incomplete", "error":
		return "image_upstream_failed_ms"
	default:
		return ""
	}
}

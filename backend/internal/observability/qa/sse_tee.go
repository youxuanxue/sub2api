package qa

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/replaycapture"
	"github.com/gin-gonic/gin"
)

const (
	contextKeyRequestBytes = "qa_request_bytes"
	contextKeyTeeWriter    = "qa_tee_writer"
)

type teeResponseWriter struct {
	replay *replaycapture.ResponseObserver
	gin.ResponseWriter
	startedAt    time.Time
	maxBodyBytes int
	responseBody bytes.Buffer
	streamOffset int
	searchOffset int
	chunks       []sseChunkBoundary
	truncated    bool
}

func newTeeResponseWriter(rw gin.ResponseWriter, maxBodyBytes int) *teeResponseWriter {
	return &teeResponseWriter{
		ResponseWriter: rw,
		startedAt:      time.Now(),
		maxBodyBytes:   maxBodyBytes,
	}
}

func (w *teeResponseWriter) Write(p []byte) (int, error) {
	w.capture(p)
	n, err := w.ResponseWriter.Write(p)
	if w.replay != nil {
		w.replay.Write(w.Header().Get("Content-Type"), p[:n])
		if err != nil || n != len(p) {
			w.replay.Fail()
		}
	}
	return n, err
}

func (w *teeResponseWriter) WriteString(s string) (int, error) {
	w.capture([]byte(s))
	n, err := w.ResponseWriter.WriteString(s)
	if w.replay != nil {
		w.replay.Write(w.Header().Get("Content-Type"), []byte(s[:n]))
		if err != nil || n != len(s) {
			w.replay.Fail()
		}
	}
	return n, err
}

// A boundary references the bounded response buffer rather than retaining a
// second copy of every SSE frame or an independently growing pending buffer.
type sseChunkBoundary struct {
	end        int
	receivedAt int64
}

const maxCapturedSSEChunks = 4096

func (w *teeResponseWriter) capture(p []byte) {
	if len(p) == 0 || w.maxBodyBytes <= 0 {
		return
	}
	remaining := w.maxBodyBytes - w.responseBody.Len()
	if remaining <= 0 {
		w.truncated = true
		return
	}
	if len(p) > remaining {
		p = p[:remaining]
		w.truncated = true
	}
	_, _ = w.responseBody.Write(p)
	if !strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
		return
	}
	for len(w.chunks) < maxCapturedSSEChunks {
		data := w.responseBody.Bytes()[w.searchOffset:]
		idx := bytes.Index(data, []byte("\n\n"))
		if idx < 0 {
			// Revisit only the last byte, which might begin a delimiter split
			// across writes. Repeated one-byte writes must not rescan the body.
			w.searchOffset = max(w.streamOffset, w.responseBody.Len()-1)
			break
		}
		w.streamOffset = w.searchOffset + idx + 2
		w.searchOffset = w.streamOffset
		w.chunks = append(w.chunks, sseChunkBoundary{end: w.streamOffset, receivedAt: time.Since(w.startedAt).Milliseconds()})
	}
	if len(w.chunks) == maxCapturedSSEChunks && w.streamOffset < w.responseBody.Len() {
		w.truncated = true
	}
}

func (w *teeResponseWriter) snapshot() ([]byte, []RawSSEChunk, bool) {
	body := append([]byte(nil), w.responseBody.Bytes()...)
	chunks := make([]RawSSEChunk, 0, len(w.chunks))
	start := 0
	for _, boundary := range w.chunks {
		chunks = append(chunks, RawSSEChunk{Bytes: body[start:boundary.end:boundary.end], RecvAtMs: boundary.receivedAt})
		start = boundary.end
	}
	return body, chunks, w.truncated
}

func Middleware(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || !svc.Enabled() {
			c.Next()
			return
		}

		c.Set("qa_ingress_method", c.Request.Method)
		c.Set("qa_ingress_path", replayRequestPath(c.Request.URL))
		// traj/synth opt-in 请求用更高的捕获上限，避免长 thinking 被截断；
		// opt-in 信号同 captureSynthHeaders（X-Synth-Session / X-Synth-Pipeline）。
		maxBytes := svc.BodyMaxBytes()
		if requestIsSynthOptIn(c) {
			maxBytes = svc.OptInBodyMaxBytes()
		}
		// Capture only bytes consumed downstream. In particular, rejected auth
		// requests must not be eagerly read or decompressed by observability.
		captureLimit := maxBytes
		var replayIn *replayIngress
		if svc.replay != nil && svc.replay.Acquire() {
			defer svc.replay.Release()
			replayIn = &replayIngress{method: c.Request.Method, path: replayRequestPath(c.Request.URL), headers: replayHeaders(c.Request.Header), observer: &replaycapture.ResponseObserver{AllowText: replayAudioPath(replayRequestPath(c.Request.URL))}}
			captureLimit = max(captureLimit, replaycapture.MaxBodyBytes+1)
		}
		var requestCapture *requestCaptureReader
		var captureRequest *http.Request
		if c.Request != nil && c.Request.Body != nil {
			// Handlers may remove Content-Encoding after decompressing the wire body.
			captureRequest = &http.Request{Header: c.Request.Header.Clone(), ContentLength: c.Request.ContentLength}
			requestCapture = &requestCaptureReader{ReadCloser: c.Request.Body, limit: captureLimit}
			c.Request.Body = requestCapture
		}
		tee := newTeeResponseWriter(c.Writer, maxBytes)
		if replayIn != nil {
			replayIn.reader = requestCapture
			replayIn.request = captureRequest
			tee.replay = replayIn.observer
			c.Set(replayIngressKey, replayIn)
		}
		c.Writer = tee
		c.Set(contextKeyTeeWriter, tee)
		c.Next()
		if requestCapture != nil {
			c.Set(contextKeyRequestBytes, qaRequestCaptureBytes(captureRequest, requestCapture.body.Bytes()[:min(requestCapture.body.Len(), maxBytes)], maxBytes))
		}
		svc.CaptureFromContext(c)
	}
}

type requestCaptureReader struct {
	total int64
	eof   bool
	io.ReadCloser
	body  bytes.Buffer
	limit int
}

func (r *requestCaptureReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.total += int64(n)
	if err == io.EOF {
		r.eof = true
	}
	if remaining := r.limit - r.body.Len(); remaining > 0 && n > 0 {
		_, _ = r.body.Write(p[:min(n, remaining)])
	}
	return n, err
}

func qaRequestCaptureBytes(req *http.Request, raw []byte, limits ...int) []byte {
	if req == nil || len(raw) == 0 {
		return raw
	}
	contentType, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/") {
		return qaOmittedBodyBytes("multipart_body_omitted", map[string]string{
			"content_type": contentType,
		})
	}

	encoding := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Encoding")))
	if encoding == "" || encoding == "identity" {
		return raw
	}
	limit := int64(256 * 1024)
	if len(limits) > 0 && limits[0] > 0 {
		limit = int64(limits[0])
	}
	decoded, err := pkghttputil.DecodeContentEncodedBodyPrefix(encoding, raw, limit)
	if err != nil {
		return qaOmittedBodyBytes("content_encoding_decode_failed", map[string]string{
			"content_encoding": encoding,
		})
	}
	return decoded
}

func qaOmittedBodyBytes(reason string, extra map[string]string) []byte {
	payload := map[string]any{
		"_qa_body_omitted": true,
		"reason":           reason,
	}
	for key, value := range extra {
		if strings.TrimSpace(value) != "" {
			payload[key] = value
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"_qa_body_omitted":true,"reason":"capture_metadata_unavailable"}`)
	}
	return raw
}

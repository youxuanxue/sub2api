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

	"github.com/gin-gonic/gin"
)

const (
	contextKeyRequestBytes = "qa_request_bytes"
	contextKeyTeeWriter    = "qa_tee_writer"
)

type teeResponseWriter struct {
	gin.ResponseWriter
	startedAt    time.Time
	maxBodyBytes int
	responseBody bytes.Buffer
	pendingChunk bytes.Buffer
	chunks       []RawSSEChunk
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
	return w.ResponseWriter.Write(p)
}

func (w *teeResponseWriter) WriteString(s string) (int, error) {
	w.capture([]byte(s))
	return w.ResponseWriter.WriteString(s)
}

func (w *teeResponseWriter) capture(p []byte) {
	if len(p) == 0 || w.maxBodyBytes <= 0 {
		return
	}
	if w.responseBody.Len() < w.maxBodyBytes {
		remaining := w.maxBodyBytes - w.responseBody.Len()
		if len(p) > remaining {
			_, _ = w.responseBody.Write(p[:remaining])
			w.truncated = true
		} else {
			_, _ = w.responseBody.Write(p)
		}
	} else {
		w.truncated = true
	}

	_, _ = w.pendingChunk.Write(p)
	for {
		data := w.pendingChunk.Bytes()
		idx := bytes.Index(data, []byte("\n\n"))
		if idx < 0 {
			break
		}
		chunk := append([]byte(nil), data[:idx+2]...)
		w.chunks = append(w.chunks, RawSSEChunk{
			Bytes:    chunk,
			RecvAtMs: time.Since(w.startedAt).Milliseconds(),
		})
		w.pendingChunk.Next(idx + 2)
	}
}

func (w *teeResponseWriter) snapshot() ([]byte, []RawSSEChunk, bool) {
	body := append([]byte(nil), w.responseBody.Bytes()...)
	chunks := make([]RawSSEChunk, len(w.chunks))
	copy(chunks, w.chunks)
	return body, chunks, w.truncated
}

func Middleware(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || !svc.Enabled() {
			c.Next()
			return
		}

		if c.Request != nil && c.Request.Body != nil {
			raw, err := io.ReadAll(c.Request.Body)
			if err == nil {
				c.Request.Body = io.NopCloser(bytes.NewReader(raw))
				c.Set(contextKeyRequestBytes, qaRequestCaptureBytes(c.Request, raw))
			}
		}

		tee := newTeeResponseWriter(c.Writer, svc.BodyMaxBytes())
		c.Writer = tee
		c.Set(contextKeyTeeWriter, tee)
		c.Next()
		svc.CaptureFromContext(c)
	}
}

func qaRequestCaptureBytes(req *http.Request, raw []byte) []byte {
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
	decoded, err := pkghttputil.DecodeContentEncodedBody(encoding, raw)
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

package middleware

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Upstream adapters may copy their response headers after RequestLogger runs.
// Restore the local identity at the commit boundary for both JSON and SSE.
type requestIDResponseWriter struct {
	gin.ResponseWriter
	requestID string
}

func (w *requestIDResponseWriter) prepareHeaders() {
	if w.Status() == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
		w.Header().Set("Retry-After", strconv.Itoa(apiKeyQuotaRetryAfterSeconds))
	}
	w.Header().Set(requestIDHeader, w.requestID)
}

func (w *requestIDResponseWriter) WriteHeader(status int) {
	w.Header().Set(requestIDHeader, w.requestID)
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestIDResponseWriter) WriteHeaderNow() {
	w.prepareHeaders()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *requestIDResponseWriter) Write(body []byte) (int, error) {
	w.prepareHeaders()
	return w.ResponseWriter.Write(body)
}

func (w *requestIDResponseWriter) WriteString(body string) (int, error) {
	w.prepareHeaders()
	return w.ResponseWriter.WriteString(body)
}

func (w *requestIDResponseWriter) Flush() {
	w.prepareHeaders()
	w.ResponseWriter.Flush()
}

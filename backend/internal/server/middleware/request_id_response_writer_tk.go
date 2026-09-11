package middleware

import "github.com/gin-gonic/gin"

// Upstream adapters may copy their response headers after RequestLogger runs.
// Restore the local identity at the commit boundary for both JSON and SSE.
type requestIDResponseWriter struct {
	gin.ResponseWriter
	requestID string
}

func (w *requestIDResponseWriter) restoreRequestID() {
	w.Header().Set(requestIDHeader, w.requestID)
}

func (w *requestIDResponseWriter) WriteHeader(status int) {
	w.restoreRequestID()
	w.ResponseWriter.WriteHeader(status)
}

func (w *requestIDResponseWriter) WriteHeaderNow() {
	w.restoreRequestID()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *requestIDResponseWriter) Write(body []byte) (int, error) {
	w.restoreRequestID()
	return w.ResponseWriter.Write(body)
}

func (w *requestIDResponseWriter) WriteString(body string) (int, error) {
	w.restoreRequestID()
	return w.ResponseWriter.WriteString(body)
}

func (w *requestIDResponseWriter) Flush() {
	w.restoreRequestID()
	w.ResponseWriter.Flush()
}

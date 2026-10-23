package bridge

import (
	"net/http"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
)

// upstreamHeaderFilter drops supplier identity headers before they reach the
// client. New API's response copy writes every upstream header onto the gin
// writer; the native OpenAI path already allowlists headers, and this wrapper
// applies that same policy on the adaptor path.
type upstreamHeaderFilter struct {
	gin.ResponseWriter
	preexisting http.Header
	once        sync.Once
}

func filterUpstreamResponseHeaders(c *gin.Context) func() {
	if c == nil || c.Writer == nil {
		return func() {}
	}
	original := c.Writer
	c.Writer = &upstreamHeaderFilter{
		ResponseWriter: original,
		preexisting:    cloneHeader(original.Header()),
	}
	return func() { c.Writer = original }
}

func cloneHeader(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func (w *upstreamHeaderFilter) strip() {
	w.once.Do(func() {
		responseheaders.RetainClientResponseHeaders(w.Header(), w.preexisting, nil)
	})
}

func (w *upstreamHeaderFilter) WriteHeader(code int) {
	w.strip()
	w.ResponseWriter.WriteHeader(code)
}

func (w *upstreamHeaderFilter) WriteHeaderNow() {
	w.strip()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *upstreamHeaderFilter) Write(p []byte) (int, error) {
	w.strip()
	return w.ResponseWriter.Write(p)
}

func (w *upstreamHeaderFilter) WriteString(s string) (int, error) {
	w.strip()
	return w.ResponseWriter.WriteString(s)
}

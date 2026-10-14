package middleware

import (
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const ingressTimingKey = "tk.ingress_timing"

// ingressTiming observes the original wire body, before replay/decompression.
// Reading remains demand-driven: auth rejection never drains an unread upload.
// Read wall time includes the transport wait; it is not a CPU-time measurement.
type ingressTiming struct {
	mu                     sync.Mutex
	started                time.Time
	bodyRead               time.Duration
	bodyBytes              int64
	bodyReads              int
	firstRead, lastRead    time.Duration
	beforeRouting, routing time.Duration
	routingObserved        bool
}

type ingressTimingBody struct {
	io.ReadCloser
	timing *ingressTiming
}

func (b *ingressTimingBody) Read(p []byte) (int, error) {
	start := time.Now()
	n, err := b.ReadCloser.Read(p)
	end := time.Now()
	b.timing.mu.Lock()
	if b.timing.bodyReads == 0 {
		b.timing.firstRead = start.Sub(b.timing.started)
	}
	b.timing.bodyReads++
	b.timing.bodyBytes += int64(n)
	b.timing.bodyRead += end.Sub(start)
	b.timing.lastRead = end.Sub(b.timing.started)
	b.timing.mu.Unlock()
	return n, err
}

func observeIngress(c *gin.Context, start time.Time) {
	timing := &ingressTiming{started: start}
	c.Set(ingressTimingKey, timing)
	if c.Request != nil && c.Request.Body != nil && c.Request.Body != http.NoBody {
		c.Request.Body = &ingressTimingBody{ReadCloser: c.Request.Body, timing: timing}
	}
}

func ingressTimingFromContext(c *gin.Context) *ingressTiming {
	v, ok := c.Get(ingressTimingKey)
	if !ok {
		return nil
	}
	timing, _ := v.(*ingressTiming)
	return timing
}

func observeIngressRouting(c *gin.Context) func() {
	timing := ingressTimingFromContext(c)
	if timing == nil {
		return func() {}
	}
	start := time.Now()
	timing.mu.Lock()
	timing.beforeRouting = start.Sub(timing.started)
	timing.routingObserved = true
	timing.mu.Unlock()
	return func() {
		timing.mu.Lock()
		timing.routing += time.Since(start)
		timing.mu.Unlock()
	}
}

func ingressTimingFields(c *gin.Context) []zap.Field {
	timing := ingressTimingFromContext(c)
	if timing == nil {
		return nil
	}
	timing.mu.Lock()
	defer timing.mu.Unlock()
	fields := []zap.Field{
		zap.Int64("ingress_body_read_ms", timing.bodyRead.Milliseconds()),
		zap.Int64("ingress_body_bytes", timing.bodyBytes),
		zap.Int("ingress_body_read_calls", timing.bodyReads),
	}
	if timing.bodyReads > 0 {
		fields = append(fields, zap.Int64("ingress_body_first_read_ms", timing.firstRead.Milliseconds()), zap.Int64("ingress_body_last_read_ms", timing.lastRead.Milliseconds()))
	}
	if timing.routingObserved {
		fields = append(fields, zap.Int64("ingress_before_routing_ms", timing.beforeRouting.Milliseconds()), zap.Int64("ingress_routing_ms", timing.routing.Milliseconds()))
	}
	return fields
}

package service

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// ImageIngressPhase timings describe work before universal routing returns.
// They are diagnostic spans, not additions to usage duration or billing fields.
type ImageIngressPhase string

const (
	ImageIngressKeyAuth    ImageIngressPhase = "image_ingress_key_auth_ms"
	ImageIngressSelection  ImageIngressPhase = "image_ingress_selection_ms"
	ImageIngressCandidates ImageIngressPhase = "image_ingress_candidates_ms"
	ImageIngressBind       ImageIngressPhase = "image_ingress_bind_ms"
)

type imageIngressTimingKey struct{}

type imageIngressTiming struct {
	mu            sync.Mutex
	now           func() time.Time
	start         time.Time
	finished      bool
	phases        map[ImageIngressPhase]time.Duration
	ingress       time.Duration
	bodyRead      time.Duration
	bodyBytes     int64
	contentLength int64
}

// WithImageIngressTiming observes only synchronous Images requests. The body
// wrapper counts actual wire-body reads, never eagerly consumes or retains data,
// and remains below replay/decompression wrappers so replay is not counted twice.
func WithImageIngressTiming(req *http.Request, start time.Time) *http.Request {
	if req == nil || req.URL == nil || req.Method != http.MethodPost {
		return req
	}
	switch req.URL.Path {
	case "/v1/images/generations", "/v1/images/edits", "/images/generations", "/images/edits":
	default:
		return req
	}
	if imageIngressTimingFrom(req.Context()) != nil {
		return req
	}
	timing := &imageIngressTiming{now: time.Now, start: start, phases: make(map[ImageIngressPhase]time.Duration), contentLength: req.ContentLength}
	req = req.WithContext(context.WithValue(req.Context(), imageIngressTimingKey{}, timing))
	if req.Body != nil {
		req.Body = &imageIngressBodyReader{ReadCloser: req.Body, timing: timing}
	}
	return req
}

func imageIngressTimingFrom(ctx context.Context) *imageIngressTiming {
	timing, _ := ctx.Value(imageIngressTimingKey{}).(*imageIngressTiming)
	return timing
}

// BeginImageIngressPhase is a no-op outside Images ingress, including subsequent
// handler selections/retries. Its completion callback is safe to call twice.
func BeginImageIngressPhase(ctx context.Context, phase ImageIngressPhase) func() {
	timing := imageIngressTimingFrom(ctx)
	if timing == nil {
		return func() {}
	}
	timing.mu.Lock()
	active := !timing.finished
	timing.mu.Unlock()
	if !active {
		return func() {}
	}
	start := timing.now()
	var once sync.Once
	return func() {
		once.Do(func() {
			elapsed := timing.now().Sub(start)
			timing.mu.Lock()
			timing.phases[phase] += elapsed
			timing.mu.Unlock()
		})
	}
}

// FinishImageIngress freezes the pre-routing span on either success or error.
// Body read timing is independent: direct paths may consume their body later.
func FinishImageIngress(ctx context.Context) {
	timing := imageIngressTimingFrom(ctx)
	if timing == nil {
		return
	}
	timing.mu.Lock()
	defer timing.mu.Unlock()
	if !timing.finished {
		timing.ingress = timing.now().Sub(timing.start)
		timing.finished = true
	}
}

// ImageIngressTimingFields contains numbers only, never request content or
// credentials. Missing phases were not observed; zero means an observed <1ms
// phase. Spans can overlap (e.g. body reading within ingress), so do not sum them.
func ImageIngressTimingFields(ctx context.Context) map[string]int64 {
	timing := imageIngressTimingFrom(ctx)
	if timing == nil {
		return nil
	}
	timing.mu.Lock()
	defer timing.mu.Unlock()
	fields := map[string]int64{
		"image_request_body_read_ms":   timing.bodyRead.Milliseconds(),
		"image_request_body_bytes":     timing.bodyBytes,
		"image_request_content_length": timing.contentLength,
	}
	if timing.finished {
		fields["image_ingress_ms"] = timing.ingress.Milliseconds()
	}
	for phase, duration := range timing.phases {
		fields[string(phase)] = duration.Milliseconds()
	}
	return fields
}

type imageIngressBodyReader struct {
	io.ReadCloser
	timing *imageIngressTiming
}

func (r *imageIngressBodyReader) Read(p []byte) (int, error) {
	start := r.timing.now()
	n, err := r.ReadCloser.Read(p)
	elapsed := r.timing.now().Sub(start)
	r.timing.mu.Lock()
	r.timing.bodyRead += elapsed
	r.timing.bodyBytes += int64(n)
	r.timing.mu.Unlock()
	return n, err
}

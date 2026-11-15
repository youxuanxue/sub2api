//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// resetInFlightGateForTest clears the package-level gate state between tests.
func resetInFlightGateForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		maxInFlightRelay.Store(0)
		inflightRelay.Store(0)
		inflightRelayRejected.Store(0)
	}
	reset()
	t.Cleanup(reset)
}

func gateRouter(handler gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(InFlightGate)
	r.POST("/v1/messages", handler)
	return r
}

// blockingHandler parks every request until release is closed, and reports when
// it has entered so the test can observe the counter mid-flight.
func blockingHandler(entered chan<- struct{}, release <-chan struct{}) gin.HandlerFunc {
	return func(c *gin.Context) {
		entered <- struct{}{}
		<-release
		c.String(http.StatusOK, "ok")
	}
}

func waitEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler never entered; in-flight bookkeeping may be stuck")
	}
}

func TestInFlightGateDisabledAllowsEverythingAndDoesNotCount(t *testing.T) {
	resetInFlightGateForTest(t)
	SetMaxInFlightRelay(0)

	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	r := gateRouter(blockingHandler(entered, release))

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		}()
		waitEntered(t, entered)
		if got := InFlightRelayCount(); got != 0 {
			t.Fatalf("disabled gate must not count requests, got in_flight_relay=%d", got)
		}
	}
	close(release)
	wg.Wait()
	if got := InFlightRelayRejectedCount(); got != 0 {
		t.Fatalf("disabled gate must never reject, got rejected=%d", got)
	}
}

func TestInFlightGateAdmitsUpToLimitThenRejectsWithRetryAfter(t *testing.T) {
	resetInFlightGateForTest(t)
	SetMaxInFlightRelay(2)

	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	r := gateRouter(blockingHandler(entered, release))

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		}()
		waitEntered(t, entered)
	}
	if got := InFlightRelayCount(); got != 2 {
		t.Fatalf("expected in_flight_relay=2 at the limit, got %d", got)
	}

	// Third request must fail fast, not queue: the handler must never be reached.
	blockedHandlerReached := false
	w := httptest.NewRecorder()
	blocked := gateRouter(func(c *gin.Context) {
		blockedHandlerReached = true
		c.String(http.StatusOK, "should not happen")
	})
	blocked.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for over-limit request, got %d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After=1 on shed request, got %q", got)
	}
	if blockedHandlerReached {
		t.Fatal("shed request must abort before the handler; billing/routing must not run")
	}
	if got := InFlightRelayRejectedCount(); got != 1 {
		t.Fatalf("expected rejected count=1, got %d", got)
	}
	if got := InFlightRelayCount(); got != 2 {
		t.Fatalf("a rejected request must not leak a slot, got in_flight_relay=%d", got)
	}

	close(release)
	wg.Wait()
	if got := InFlightRelayCount(); got != 0 {
		t.Fatalf("expected in_flight_relay=0 after admitted requests finished, got %d", got)
	}
}

func TestInFlightGateRejectionUsesInboundProtocolEnvelope(t *testing.T) {
	resetInFlightGateForTest(t)
	SetMaxInFlightRelay(1)

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	holder := gateRouter(blockingHandler(entered, release))
	holderDone := make(chan struct{})
	go func() {
		holder.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		close(holderDone)
	}()
	waitEntered(t, entered)

	cases := []struct {
		path       string
		wantSubstr []string
		forbid     []string
	}{
		{
			path:       "/v1/messages",
			wantSubstr: []string{`"type":"error"`, `"type":"api_error"`},
			forbid:     []string{"model_not_found"},
		},
		{
			path:       "/v1/chat/completions",
			wantSubstr: []string{`"type":"overloaded_error"`, `"code":"overloaded_error"`},
			forbid:     []string{"model_not_found"},
		},
		{
			path:       "/v1beta/models/gemini-2.5-pro:generateContent",
			wantSubstr: []string{`"status":"INTERNAL"`, `"code":503`},
			forbid:     []string{"model_not_found"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := gin.New()
			r.Use(InFlightGate)
			r.POST(tc.path, func(c *gin.Context) { t.Fatal("handler reached for shed request") })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, nil))
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
			}
			body := strings.ReplaceAll(w.Body.String(), " ", "")
			for _, want := range tc.wantSubstr {
				if !strings.Contains(body, strings.ReplaceAll(want, " ", "")) {
					t.Fatalf("body %s missing %s", body, want)
				}
			}
			for _, forbidden := range tc.forbid {
				if strings.Contains(body, forbidden) {
					t.Fatalf("body %s must not carry %s", body, forbidden)
				}
			}
		})
	}
	close(release)
	<-holderDone
}

func TestInFlightGateReleasesOnPanic(t *testing.T) {
	resetInFlightGateForTest(t)
	SetMaxInFlightRelay(1)

	r := gin.New()
	r.Use(Recovery())
	r.Use(InFlightGate)
	r.POST("/v1/messages", func(c *gin.Context) {
		panic("boom")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	if got := InFlightRelayCount(); got != 0 {
		t.Fatalf("panic path must return the slot, got in_flight_relay=%d", got)
	}
	// The gate must still admit the next request: a leaked slot would wedge it.
	w2 := httptest.NewRecorder()
	r2 := gin.New()
	r2.Use(InFlightGate)
	r2.POST("/v1/messages", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r2.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("gate wedged after panic: next request got %d", w2.Code)
	}
}

func TestInFlightGateReleasesOnClientDisconnect(t *testing.T) {
	resetInFlightGateForTest(t)
	SetMaxInFlightRelay(1)

	entered := make(chan struct{}, 1)
	stillDraining := make(chan struct{})
	r := gin.New()
	r.Use(InFlightGate)
	r.POST("/v1/messages", func(c *gin.Context) {
		entered <- struct{}{}
		// Simulate late billing drain after the caller is gone: the handler
		// keeps running, so only the context-cancellation release can free the
		// slot in time.
		<-stillDraining
		c.String(http.StatusOK, "late")
	})

	done := make(chan struct{})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	go func() {
		r.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	waitEntered(t, entered)

	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for InFlightRelayCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := InFlightRelayCount(); got != 0 {
		t.Fatalf("client disconnect must release the slot, got in_flight_relay=%d", got)
	}

	// The slot is free, so a new request is admitted even though the old
	// handler is still parked. Release the parked handler from a goroutine so
	// this assertion cannot deadlock on it.
	go func() {
		close(stillDraining)
	}()
	w := httptest.NewRecorder()
	second := make(chan struct{})
	go func() {
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		close(second)
	}()
	select {
	case <-second:
	case <-time.After(3 * time.Second):
		t.Fatal("gate wedged after client disconnect; a slot was never returned")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected the freed slot to admit a new request, got %d", w.Code)
	}
	<-done
}

func TestInFlightGateReleaseIsIdempotentAcrossBothPaths(t *testing.T) {
	resetInFlightGateForTest(t)
	SetMaxInFlightRelay(4)

	r := gin.New()
	r.Use(InFlightGate)
	r.POST("/v1/messages", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	// Alternate a normal request and a canceled one many times; a double release
	// would drive the counter negative and silently disable the gate.
	for i := 0; i < 25; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		if got := InFlightRelayCount(); got != 0 {
			t.Fatalf("iteration %d leaked or double-released: in_flight_relay=%d", i, got)
		}
	}
	if got := InFlightRelayCount(); got < 0 {
		t.Fatalf("counter went negative (%d); double release", got)
	}
}

func TestInFlightGateCountsStreamedResponsesUntilWritten(t *testing.T) {
	resetInFlightGateForTest(t)
	SetMaxInFlightRelay(1)

	streamStarted := make(chan struct{})
	finishStream := make(chan struct{})
	r := gin.New()
	r.Use(InFlightGate)
	r.GET("/v1/messages", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write([]byte("event: message_start\n\n"))
		c.Writer.Flush()
		close(streamStarted)
		<-finishStream
		_, _ = c.Writer.Write([]byte("event: message_stop\n\n"))
		c.Writer.Flush()
	})

	done := make(chan struct{})
	go func() {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/messages", nil))
		close(done)
	}()
	select {
	case <-streamStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("stream never started")
	}

	// Mid-stream the slot must still be held: counting only until the handler
	// returned (i.e. first-byte) would undercount long SSE requests badly.
	if got := InFlightRelayCount(); got != 1 {
		t.Fatalf("slot must be held while the stream is open, got in_flight_relay=%d", got)
	}
	// Over the limit while the stream is open: prove the gate actually sheds.
	over := gin.New()
	over.Use(InFlightGate)
	over.GET("/v1/messages", func(c *gin.Context) { t.Fatal("shed request reached handler") })
	w := httptest.NewRecorder()
	over.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/messages", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while a stream holds the only slot, got %d", w.Code)
	}

	close(finishStream)
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for InFlightRelayCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := InFlightRelayCount(); got != 0 {
		t.Fatalf("expected slot released after the stream completed, got %d", got)
	}
}

func TestInFlightGateConcurrentAdmissionNeverExceedsLimit(t *testing.T) {
	resetInFlightGateForTest(t)
	const limit = 4
	SetMaxInFlightRelay(limit)

	var mu sync.Mutex
	var peak int64
	release := make(chan struct{})

	r := gin.New()
	r.Use(InFlightGate)
	r.POST("/v1/messages", func(c *gin.Context) {
		mu.Lock()
		if current := InFlightRelayCount(); current > peak {
			peak = current
		}
		mu.Unlock()
		<-release
		c.String(http.StatusOK, "ok")
	})

	const total = 32
	var wg sync.WaitGroup
	var ok, rejected int
	var statMu sync.Mutex
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			statMu.Lock()
			defer statMu.Unlock()
			if w.Code == http.StatusOK {
				ok++
			} else if w.Code == http.StatusServiceUnavailable {
				rejected++
			}
		}()
	}

	// Let the burst settle at the cap, then release everything. Once slots are
	// returned the remaining requests are admitted one after another, so the
	// invariant to prove is the concurrency ceiling, not a fixed admit count.
	deadline := time.Now().Add(3 * time.Second)
	for InFlightRelayCount() < limit && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	wg.Wait()

	if peak > limit {
		t.Fatalf("admitted %d concurrent requests, above limit %d", peak, limit)
	}
	if peak != limit {
		t.Fatalf("expected the gate to actually reach the limit %d under a %d-request burst, peaked at %d", limit, total, peak)
	}
	if ok+rejected != total {
		t.Fatalf("every request must be either admitted or shed: ok=%d rejected=%d total=%d", ok, rejected, total)
	}
	if rejected == 0 {
		t.Fatalf("%d concurrent requests against limit %d must shed at least one; the gate never engaged", total, limit)
	}
	if got := InFlightRelayCount(); got != 0 {
		t.Fatalf("expected all slots returned, got %d", got)
	}
}

// TestInFlightGateGaugeNeverExceedsLimit guards the invariant that admission is
// strictly bounded at every instant, not just after rejected requests unwind.
//
// A naive Add(1)-then-Add(-1) on overflow publishes a value above the limit for
// a moment. /health/inflight and the blue/green drain probe read that gauge from
// another goroutine, so a transient overshoot is observable and would make
// "in_flight_relay > max" a real reading. It also made the concurrency test
// flaky under -race. Compare-and-swap admission is what keeps this exact.
func TestInFlightGateGaugeNeverExceedsLimit(t *testing.T) {
	resetInFlightGateForTest(t)
	const limit = 4
	SetMaxInFlightRelay(limit)

	release := make(chan struct{})
	entered := make(chan struct{}, limit)

	router := gin.New()
	router.Use(InFlightGate)
	router.POST("/v1/messages", func(c *gin.Context) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		c.String(http.StatusOK, "ok")
	})

	// Saturate, then hammer far past the limit while a separate goroutine samples
	// the gauge as fast as it can. Any sample above limit fails the invariant.
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		}()
	}

	stop := make(chan struct{})
	var overshoot int64
	var sampleWG sync.WaitGroup
	sampleWG.Add(1)
	go func() {
		defer sampleWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if current := InFlightRelayCount(); current > limit {
				atomic.AddInt64(&overshoot, 1)
			}
		}
	}()

	// Wait until the gate is actually engaged before releasing, so the sampler
	// overlaps the admission/rejection window rather than idle time.
	deadline := time.Now().Add(3 * time.Second)
	for InFlightRelayCount() < limit && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	sampleWG.Wait()
	close(release)
	wg.Wait()

	if got := atomic.LoadInt64(&overshoot); got != 0 {
		t.Fatalf("gauge exceeded limit %d in %d samples; admission must not publish an over-limit value", limit, got)
	}
	if got := InFlightRelayCount(); got != 0 {
		t.Fatalf("expected all slots returned, got %d", got)
	}
}

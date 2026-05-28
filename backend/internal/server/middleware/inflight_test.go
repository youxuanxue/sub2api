//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// resetInflightForTest 在测试间清掉 package-level 状态，避免互相污染。
func resetInflightForTest(t *testing.T) {
	t.Helper()
	drainFlag.Store(false)
	inflight.Store(0)
	t.Cleanup(func() {
		drainFlag.Store(false)
		inflight.Store(0)
	})
}

func TestSetDrainAndIsDraining(t *testing.T) {
	resetInflightForTest(t)
	if IsDraining() {
		t.Fatalf("expected IsDraining()=false initially, got true")
	}
	SetDrain(true)
	if !IsDraining() {
		t.Fatalf("expected IsDraining()=true after SetDrain(true)")
	}
	SetDrain(false)
	if IsDraining() {
		t.Fatalf("expected IsDraining()=false after SetDrain(false)")
	}
}

func TestInFlightTrackerCountsBusinessRequest(t *testing.T) {
	resetInflightForTest(t)

	released := make(chan struct{})
	observed := make(chan int64, 1)

	r := gin.New()
	r.Use(InFlightTracker())
	r.GET("/work", func(c *gin.Context) {
		observed <- InFlightCount()
		<-released
		c.String(http.StatusOK, "done")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/work", nil)
	doneCh := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(doneCh)
	}()

	select {
	case got := <-observed:
		if got != 1 {
			t.Fatalf("expected in-flight=1 while handler running, got %d", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never observed; tracker may be broken")
	}

	close(released)
	<-doneCh

	if got := InFlightCount(); got != 0 {
		t.Fatalf("expected in-flight=0 after handler returns, got %d", got)
	}
}

func TestInFlightTrackerSkipsHealthProbes(t *testing.T) {
	resetInflightForTest(t)

	observedDuringHealth := make(chan int64, 3)

	r := gin.New()
	r.Use(InFlightTracker())
	r.GET("/health", func(c *gin.Context) {
		observedDuringHealth <- InFlightCount()
		c.String(http.StatusOK, "ok")
	})
	r.GET("/health/live", func(c *gin.Context) {
		observedDuringHealth <- InFlightCount()
		c.String(http.StatusOK, "alive")
	})
	r.GET("/health/inflight", func(c *gin.Context) {
		observedDuringHealth <- InFlightCount()
		c.String(http.StatusOK, "0")
	})

	for _, path := range []string{"/health", "/health/live", "/health/inflight"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("path %s: expected 200 got %d", path, w.Code)
		}
	}

	close(observedDuringHealth)
	for got := range observedDuringHealth {
		if got != 0 {
			t.Fatalf("expected in-flight=0 inside health probes, got %d", got)
		}
	}

	if got := InFlightCount(); got != 0 {
		t.Fatalf("expected in-flight=0 finally, got %d", got)
	}
}

func TestInFlightTrackerConcurrent(t *testing.T) {
	resetInflightForTest(t)

	const N = 25
	release := make(chan struct{})
	allEntered := make(chan struct{})
	var entered int64
	var enterMu sync.Mutex

	r := gin.New()
	r.Use(InFlightTracker())
	r.GET("/work", func(c *gin.Context) {
		enterMu.Lock()
		entered++
		if entered == N {
			close(allEntered)
		}
		enterMu.Unlock()
		<-release
		c.String(http.StatusOK, "done")
	})

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/work", nil)
			r.ServeHTTP(w, req)
		}()
	}

	select {
	case <-allEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("not all handlers entered in time")
	}

	if got := InFlightCount(); got != int64(N) {
		t.Fatalf("expected in-flight=%d while %d handlers parked, got %d", N, N, got)
	}

	close(release)
	wg.Wait()

	if got := InFlightCount(); got != 0 {
		t.Fatalf("expected in-flight=0 after all handlers returned, got %d", got)
	}
}

func TestIsHealthProbePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/health", true},
		{"/health/live", true},
		{"/health/inflight", true},
		{"/health/whatever", true},
		{"", false},
		{"/", false},
		{"/api/v1/messages", false},
		{"/healthcheck", false}, // 非 /health 前缀，不能误伤
		{"/healthz", false},     // 同上
	}
	for _, tc := range cases {
		got := isHealthProbePath(tc.path)
		if got != tc.want {
			t.Errorf("isHealthProbePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

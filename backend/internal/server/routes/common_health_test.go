//go:build unit

package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newCommonRouter(t *testing.T) *gin.Engine {
	t.Helper()
	r := gin.New()
	// 复刻 router.go 的中间件顺序，确保 /health/inflight 能读到 InFlightTracker 的计数。
	r.Use(middleware.InFlightTracker())
	RegisterCommonRoutes(r)
	return r
}

func resetDrainForTest(t *testing.T) {
	t.Helper()
	middleware.SetDrain(false)
	t.Cleanup(func() { middleware.SetDrain(false) })
}

func decodeJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode JSON: %v (raw=%s)", err, string(body))
	}
	return got
}

func TestHealthEndpoint_OK_WhenNotDraining(t *testing.T) {
	resetDrainForTest(t)
	r := newCommonRouter(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", w.Code, w.Body.String())
	}
	got := decodeJSON(t, w.Body.Bytes())
	if got["status"] != "ok" {
		t.Fatalf("expected status=ok got %v", got["status"])
	}
}

func TestHealthEndpoint_503_WhenDraining(t *testing.T) {
	resetDrainForTest(t)
	middleware.SetDrain(true)
	r := newCommonRouter(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 got %d body=%s", w.Code, w.Body.String())
	}
	got := decodeJSON(t, w.Body.Bytes())
	if got["status"] != "draining" {
		t.Fatalf("expected status=draining got %v", got["status"])
	}
	if _, ok := got["in_flight"]; !ok {
		t.Fatalf("expected in_flight field, got %v", got)
	}
}

func TestHealthLive_AlwaysOK(t *testing.T) {
	resetDrainForTest(t)
	r := newCommonRouter(t)

	for _, drain := range []bool{false, true} {
		middleware.SetDrain(drain)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("/health/live drain=%v: expected 200 got %d", drain, w.Code)
		}
		got := decodeJSON(t, w.Body.Bytes())
		if got["status"] != "alive" {
			t.Fatalf("/health/live drain=%v: expected alive got %v", drain, got["status"])
		}
	}
}

func TestHealthInflight_ReportsState(t *testing.T) {
	resetDrainForTest(t)
	r := newCommonRouter(t)

	// 默认状态：in_flight=0, draining=false。
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/inflight", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", w.Code)
	}
	got := decodeJSON(t, w.Body.Bytes())
	if v, _ := got["in_flight"].(float64); v != 0 {
		t.Fatalf("expected in_flight=0 got %v", got["in_flight"])
	}
	if v, _ := got["draining"].(bool); v {
		t.Fatalf("expected draining=false got %v", got["draining"])
	}

	// 切到 drain 后再读，应反映 draining=true。
	middleware.SetDrain(true)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/health/inflight", nil)
	r.ServeHTTP(w, req)
	got = decodeJSON(t, w.Body.Bytes())
	if v, _ := got["draining"].(bool); !v {
		t.Fatalf("expected draining=true after SetDrain(true), got %v", got["draining"])
	}
}

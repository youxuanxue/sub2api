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
	// /health 是公开端点，body 只反映 ready/draining 二态——不能携带 in_flight
	// 数（那是内部 telemetry，只在 /health/inflight 经 loopback 暴露）。
	if _, ok := got["in_flight"]; ok {
		t.Fatalf("/health 503 body must not expose in_flight count, got %v", got)
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

// newLoopbackRequest 构造一个 RemoteAddr=127.0.0.1 的请求，模拟 docker exec
// 在容器里 wget localhost:8080 的路径。httptest.NewRequest 默认 RemoteAddr=
// 192.0.2.1（TEST-NET-1），不会被 isLoopbackRemote 视为 loopback。
func newLoopbackRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	return req
}

func TestHealthInflight_ReportsState_OnLoopback(t *testing.T) {
	resetDrainForTest(t)
	r := newCommonRouter(t)

	// 默认状态：in_flight=0, draining=false。
	w := httptest.NewRecorder()
	r.ServeHTTP(w, newLoopbackRequest(http.MethodGet, "/health/inflight"))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", w.Code, w.Body.String())
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
	r.ServeHTTP(w, newLoopbackRequest(http.MethodGet, "/health/inflight"))
	got = decodeJSON(t, w.Body.Bytes())
	if v, _ := got["draining"].(bool); !v {
		t.Fatalf("expected draining=true after SetDrain(true), got %v", got["draining"])
	}
}

func TestHealthInflight_404_OffLoopback(t *testing.T) {
	resetDrainForTest(t)
	r := newCommonRouter(t)

	// 模拟 Caddy → container 的请求（Docker bridge IP），不应能拿到 inflight。
	cases := []string{
		"172.18.0.5:54321", // 典型 docker bridge
		"10.0.1.42:443",    // VPC 内网
		"192.0.2.1:80",     // 公网（TEST-NET-1）
		"2001:db8::1:42",   // IPv6 公网
		"203.0.113.5:443",  // TEST-NET-3
		"unparseable",      // 异常 RemoteAddr 也要拒
	}
	for _, ra := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health/inflight", nil)
		req.RemoteAddr = ra
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("RemoteAddr=%q: expected 404 got %d body=%s", ra, w.Code, w.Body.String())
		}
	}
}

func TestIsLoopbackRemote(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:12345", true},
		{"127.0.0.5:80", true}, // 整个 127.0.0.0/8 都算 loopback
		{"[::1]:443", true},
		{"::1", true},
		{"127.0.0.1", true},
		{"172.18.0.5:80", false},
		{"10.0.0.1:443", false},
		{"192.0.2.1:1234", false}, // httptest 默认值
		{"203.0.113.5:443", false},
		{"unparseable", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isLoopbackRemote(tc.addr)
		if got != tc.want {
			t.Errorf("isLoopbackRemote(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

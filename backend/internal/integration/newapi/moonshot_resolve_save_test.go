package newapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveMoonshotRegionalBaseAtSave_FirstSuccessWins(t *testing.T) {
	// 不可 t.Parallel()：本测试与 TestResolveMoonshotRegionalBaseAtSave_BothFail 共享
	// 包级全局 moonshotProbeBasesForTest，并行会被对方 defer 置 nil 的 race 击穿，
	// 退化为打到真实 Moonshot 上游，CI 必然偶发 401。
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fail.Close)
	t.Cleanup(ok.Close)

	moonshotProbeBasesForTest = []string{fail.URL, ok.URL}
	defer func() { moonshotProbeBasesForTest = nil }()

	got, err := ResolveMoonshotRegionalBaseAtSave(context.Background(), "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if got != ok.URL {
		t.Fatalf("want winning base %q, got %q", ok.URL, got)
	}
}

func TestResolveMoonshotRegionalBaseAtSave_BothFail(t *testing.T) {
	// 同上：不可与 _FirstSuccessWins 并行，避免 moonshotProbeBasesForTest race。
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(fail.Close)

	moonshotProbeBasesForTest = []string{fail.URL, fail.URL}
	defer func() { moonshotProbeBasesForTest = nil }()

	_, err := ResolveMoonshotRegionalBaseAtSave(context.Background(), "sk-bad")
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestResolveMoonshotRegionalBaseAtSave_SingleBaseAllFail 复现 docs/bugs/2026-04-23-newapi-fifth-platform-audit.md
// 的 P0-3：原实现在错误路径上硬写 errs[0], errs[1]，bases 长度 != 2 时直接 panic
// （admin 保存账号的 HTTP 线程会 500）。本用例注入单 base 触发失败路径，断言不 panic
// 且错误信息聚合了所有探测错误。
func TestResolveMoonshotRegionalBaseAtSave_SingleBaseAllFail(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(fail.Close)

	moonshotProbeBasesForTest = []string{fail.URL}
	defer func() { moonshotProbeBasesForTest = nil }()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("error path must not panic on len(bases)=1, got: %v", r)
		}
	}()

	_, err := ResolveMoonshotRegionalBaseAtSave(context.Background(), "sk-bad")
	if err == nil {
		t.Fatal("expected error when single base fails")
	}
	if msg := err.Error(); msg == "" {
		t.Fatalf("error must include the failed base, got: %q", msg)
	}
}

// TestResolveMoonshotRegionalBaseAtSave_ThreeBasesAllFail 验证 N>2 base 也走聚合错误路径。
// 这是为防止未来新增 Moonshot 区域（如 .com.cn 反代）时退化为 panic 而设的护栏。
func TestResolveMoonshotRegionalBaseAtSave_ThreeBasesAllFail(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(fail.Close)

	moonshotProbeBasesForTest = []string{fail.URL, fail.URL, fail.URL}
	defer func() { moonshotProbeBasesForTest = nil }()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("error path must not panic on len(bases)=3, got: %v", r)
		}
	}()

	_, err := ResolveMoonshotRegionalBaseAtSave(context.Background(), "sk-bad")
	if err == nil {
		t.Fatal("expected error when all 3 bases fail")
	}
}

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

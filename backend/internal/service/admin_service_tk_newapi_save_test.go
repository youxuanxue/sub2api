package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapifusion "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// TestResolveNewAPIMoonshotBaseURLOnSave_MutatesCredentialsToWinningRegion is
// the service-layer regression test for Bug B. It asserts that when admin
// saves a newapi/Moonshot account whose configured base_url is the wrong
// official region, the helper rewrites credentials["base_url"] in-place so
// accountRepo.Create / accountRepo.Update persists the correct region. The
// upstream-package helper test in moonshot_resolve_save_helper_test.go covers
// the resolve primitive; this test specifically pins the call-site
// integration that the previous code was missing.
func TestResolveNewAPIMoonshotBaseURLOnSave_MutatesCredentialsToWinningRegion(t *testing.T) {
	// Cannot run in parallel: shares moonshotProbeBasesForTest sentinel.
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "wrong region", http.StatusUnauthorized)
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

	newapifusion.SetMoonshotProbeBasesForTest([]string{fail.URL, ok.URL})
	t.Cleanup(func() { newapifusion.SetMoonshotProbeBasesForTest(nil) })

	account := &Account{
		Name:        "moonshot-int-key",
		Platform:    PlatformNewAPI,
		ChannelType: newapiconstant.ChannelTypeMoonshot,
		Credentials: map[string]any{
			"base_url": "https://api.moonshot.cn",
			"api_key":  "sk-international",
		},
	}

	if err := resolveNewAPIMoonshotBaseURLOnSave(context.Background(), account); err != nil {
		t.Fatalf("resolveNewAPIMoonshotBaseURLOnSave returned error: %v", err)
	}

	got, _ := account.Credentials["base_url"].(string)
	want := strings.TrimRight(ok.URL, "/")
	if got != want {
		t.Fatalf("base_url not rewritten to winning region: want %q, got %q", want, got)
	}
}

// TestResolveNewAPIMoonshotBaseURLOnSave_NoOpForNonNewAPI confirms the helper
// is a no-op for the four legacy platforms — anthropic / gemini / openai /
// antigravity accounts must never have their credentials mutated by this
// path, even if some test happens to set channel_type=25.
func TestResolveNewAPIMoonshotBaseURLOnSave_NoOpForNonNewAPI(t *testing.T) {
	original := "https://api.anthropic.com"
	account := &Account{
		Name:        "anthropic-key",
		Platform:    PlatformAnthropic,
		ChannelType: newapiconstant.ChannelTypeMoonshot,
		Credentials: map[string]any{
			"base_url": original,
			"api_key":  "sk-anthropic",
		},
	}
	if err := resolveNewAPIMoonshotBaseURLOnSave(context.Background(), account); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := account.Credentials["base_url"].(string); got != original {
		t.Fatalf("base_url must not be touched for anthropic platform: want %q, got %q", original, got)
	}
}

// TestResolveNewAPIMoonshotBaseURLOnSave_NoOpForCustomReverseProxy preserves
// admin-supplied custom hosts. The previous behavior (no resolve at all) at
// least had this property by accident; the new wiring must keep it on
// purpose.
func TestResolveNewAPIMoonshotBaseURLOnSave_NoOpForCustomReverseProxy(t *testing.T) {
	original := "https://relay.example.com"
	account := &Account{
		Name:        "moonshot-via-proxy",
		Platform:    PlatformNewAPI,
		ChannelType: newapiconstant.ChannelTypeMoonshot,
		Credentials: map[string]any{
			"base_url": original,
			"api_key":  "sk-test",
		},
	}
	if err := resolveNewAPIMoonshotBaseURLOnSave(context.Background(), account); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := account.Credentials["base_url"].(string); got != original {
		t.Fatalf("custom reverse proxy must not be overwritten: want %q, got %q", original, got)
	}
}

// TestResolveNewAPIMoonshotBaseURLOnSave_PropagatesProbeFailure ensures the
// admin save path returns an error when both regions reject the key, instead
// of silently saving the unresolved base. This is the failure mode that
// drove the Bug B fix — relay-time 401s on every request because the saved
// account was pinned to the wrong region.
func TestResolveNewAPIMoonshotBaseURLOnSave_PropagatesProbeFailure(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "wrong region", http.StatusUnauthorized)
	}))
	t.Cleanup(fail.Close)

	newapifusion.SetMoonshotProbeBasesForTest([]string{fail.URL, fail.URL})
	t.Cleanup(func() { newapifusion.SetMoonshotProbeBasesForTest(nil) })

	account := &Account{
		Name:        "moonshot-bad-key",
		Platform:    PlatformNewAPI,
		ChannelType: newapiconstant.ChannelTypeMoonshot,
		Credentials: map[string]any{
			"base_url": "https://api.moonshot.cn",
			"api_key":  "sk-bad",
		},
	}
	err := resolveNewAPIMoonshotBaseURLOnSave(context.Background(), account)
	if err == nil {
		t.Fatal("expected probe failure to propagate to admin save path")
	}
}

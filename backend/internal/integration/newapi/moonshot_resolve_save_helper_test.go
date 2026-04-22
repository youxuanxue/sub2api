package newapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// TestMaybeResolveMoonshotBaseURLForNewAPI_ResolvesWhenChannelTypeMatches is
// the regression test for Bug B: an admin saving a newapi/Moonshot account
// with the .cn root but an .ai (international) key must end up with the .ai
// base URL persisted, instead of silently keeping the .cn root and surfacing
// 401s on every relay request.
func TestMaybeResolveMoonshotBaseURLForNewAPI_ResolvesWhenChannelTypeMatches(t *testing.T) {
	// Cannot run in parallel: this test mutates the package-level
	// moonshotProbeBasesForTest sentinel, just like the existing
	// ResolveMoonshotRegionalBaseAtSave tests.
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

	moonshotProbeBasesForTest = []string{fail.URL, ok.URL}
	defer func() { moonshotProbeBasesForTest = nil }()

	resolved, didResolve, err := MaybeResolveMoonshotBaseURLForNewAPI(
		context.Background(),
		PlatformNewAPI,
		newapiconstant.ChannelTypeMoonshot,
		"https://api.moonshot.cn", // user-typed default; key actually belongs to .ai
		"sk-international",
	)
	if err != nil {
		t.Fatalf("unexpected probe error: %v", err)
	}
	if !didResolve {
		t.Fatal("expected didResolve=true when newapi+Moonshot+official base")
	}
	if resolved != strings.TrimRight(ok.URL, "/") {
		t.Fatalf("want resolved=%q, got %q", ok.URL, resolved)
	}
}

// TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsForNonMoonshotChannelType
// guards against scope creep — newapi accounts on every other channel_type
// must NOT be probed (we don't want a Deepseek save to hit Moonshot servers).
func TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsForNonMoonshotChannelType(t *testing.T) {
	resolved, didResolve, err := MaybeResolveMoonshotBaseURLForNewAPI(
		context.Background(),
		PlatformNewAPI,
		newapiconstant.ChannelTypeOpenAI, // any non-Moonshot type
		"https://api.openai.com",
		"sk-anything",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if didResolve {
		t.Fatal("non-Moonshot channel_type must not trigger resolve")
	}
	if resolved != "" {
		t.Fatalf("non-Moonshot channel_type must return empty resolved, got %q", resolved)
	}
}

// TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsForCustomReverseProxy
// preserves the user's intent when they configured a self-hosted base URL.
// We must not silently overwrite a custom relay host with the official one.
func TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsForCustomReverseProxy(t *testing.T) {
	resolved, didResolve, err := MaybeResolveMoonshotBaseURLForNewAPI(
		context.Background(),
		PlatformNewAPI,
		newapiconstant.ChannelTypeMoonshot,
		"https://relay.example.com", // custom proxy — must not be touched
		"sk-test",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if didResolve {
		t.Fatal("custom reverse proxy host must not trigger resolve")
	}
	if resolved != "" {
		t.Fatalf("custom proxy must return empty resolved, got %q", resolved)
	}
}

// TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsWhenAPIKeyEmpty avoids the
// confusing "moonshot regional resolve: api key is empty" error from
// ResolveMoonshotRegionalBaseAtSave when the admin save path is responsible
// for credential completeness validation upstream of this helper.
func TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsWhenAPIKeyEmpty(t *testing.T) {
	resolved, didResolve, err := MaybeResolveMoonshotBaseURLForNewAPI(
		context.Background(),
		PlatformNewAPI,
		newapiconstant.ChannelTypeMoonshot,
		"https://api.moonshot.cn",
		"   ", // whitespace-only
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if didResolve {
		t.Fatal("empty api key must not trigger resolve")
	}
	if resolved != "" {
		t.Fatalf("empty api key must return empty resolved, got %q", resolved)
	}
}

// TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsForNonNewapiPlatform protects
// against a future regression where some other platform might happen to use
// channel_type=25; only the newapi fifth platform should ever invoke this.
func TestMaybeResolveMoonshotBaseURLForNewAPI_SkipsForNonNewapiPlatform(t *testing.T) {
	resolved, didResolve, err := MaybeResolveMoonshotBaseURLForNewAPI(
		context.Background(),
		"openai",
		newapiconstant.ChannelTypeMoonshot,
		"https://api.moonshot.cn",
		"sk-test",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if didResolve {
		t.Fatal("non-newapi platform must not trigger resolve")
	}
	if resolved != "" {
		t.Fatalf("non-newapi platform must return empty resolved, got %q", resolved)
	}
}

// TestMaybeResolveMoonshotBaseURLForNewAPI_PropagatesProbeFailure ensures
// that when both regions reject the key, the admin save path receives the
// underlying error instead of silently persisting the unresolved base. This
// is the failure mode that motivated Bug B in the first place.
func TestMaybeResolveMoonshotBaseURLForNewAPI_PropagatesProbeFailure(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "wrong region", http.StatusUnauthorized)
	}))
	t.Cleanup(fail.Close)

	moonshotProbeBasesForTest = []string{fail.URL, fail.URL}
	defer func() { moonshotProbeBasesForTest = nil }()

	resolved, didResolve, err := MaybeResolveMoonshotBaseURLForNewAPI(
		context.Background(),
		PlatformNewAPI,
		newapiconstant.ChannelTypeMoonshot,
		"https://api.moonshot.cn",
		"sk-bad",
	)
	if err == nil {
		t.Fatal("expected probe error to propagate")
	}
	if didResolve {
		t.Fatal("didResolve must be false on probe failure")
	}
	if resolved != "" {
		t.Fatalf("resolved must be empty on probe failure, got %q", resolved)
	}
}

//go:build unit

package bridge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// fakeXRTokenUpstream answers ONLY XRToken's ARK-compatible paths
//
//	POST /v1/contents/generations/tasks
//	GET  /v1/contents/generations/tasks/:id
//
// and 404s everything else — crucially including official Ark's
// /api/v3/contents/generations/tasks. That asymmetry is the whole point: it
// mirrors the live behavior verified against api.xrtoken.net, so a regression
// that drops the wrapper (falling back to the upstream doubao adaptor, which
// hardcodes /api/v3) fails here instead of only failing in production.
type fakeXRTokenUpstream struct {
	lastFetchPath  string
	lastAuthHeader string
	upstreamTaskID string
}

func (f *fakeXRTokenUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.lastAuthHeader = r.Header.Get("Authorization")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/contents/generations/tasks":
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"` + f.upstreamTaskID + `"}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/contents/generations/tasks/"):
		f.lastFetchPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"` + f.upstreamTaskID + `","status":"succeeded","content":{"video_url":"https://cdn.example/v.mp4"}}`))
	default:
		// Official Ark shape (/api/v3/...) lands here, exactly as on the real host.
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestXRTokenTaskAdaptor_BuildRequestURL asserts the submit path swap and the
// `/v1` de-duplication. XRToken's own SDK docs hand out
// "https://api.xrtoken.net/v1" as the base, so an admin pasting that verbatim
// must NOT produce /v1/v1/contents/... — that was the one way this override
// could silently 404.
func TestXRTokenTaskAdaptor_BuildRequestURL(t *testing.T) {
	t.Parallel()
	const want = newapiintegration.XRTokenBaseURL + "/v1/contents/generations/tasks"
	for _, base := range []string{
		newapiintegration.XRTokenBaseURL,
		newapiintegration.XRTokenBaseURL + "/",
		newapiintegration.XRTokenBaseURL + "/v1",
		newapiintegration.XRTokenBaseURL + "/v1/",
	} {
		a := newXRTokenTaskAdaptor()
		a.baseURL = newapiintegration.NormalizeXRTokenBaseURL(base)
		got, err := a.BuildRequestURL(nil)
		if err != nil {
			t.Fatalf("BuildRequestURL(base=%q) error: %v", base, err)
		}
		if got != want {
			t.Fatalf("BuildRequestURL(base=%q) = %q, want %q", base, got, want)
		}
	}
}

// TestXRTokenTaskAdaptor_BuildRequestURL_EmptyBase guards against emitting a
// relative URL ("/v1/contents/...") when base_url was never resolved: the HTTP
// client would fail with an opaque error instead of naming the cause.
func TestXRTokenTaskAdaptor_BuildRequestURL_EmptyBase(t *testing.T) {
	t.Parallel()
	a := newXRTokenTaskAdaptor()
	if _, err := a.BuildRequestURL(nil); err == nil {
		t.Fatal("expected error for empty base_url, got nil")
	}
}

// TestXRTokenTaskAdaptor_FetchTask_HitsV1Path drives the real poll path over
// real HTTP against an upstream that serves ONLY XRToken's shape. The fake
// 404s /api/v3/..., so this fails if FetchTask ever reverts to the inherited
// implementation.
//
// FetchTask takes baseUrl as an argument (DispatchVideoFetch re-resolves it
// from the persisted task registry, since a poll may run in a different
// process than the submit), so it can be pointed at httptest directly.
func TestXRTokenTaskAdaptor_FetchTask_HitsV1Path(t *testing.T) {
	upstream := &fakeXRTokenUpstream{upstreamTaskID: "cgt-xr-fetch-888"}
	srv := httptest.NewServer(upstream)
	defer srv.Close()

	// FetchTask builds its own HTTP client from new-api's global pool.
	// DispatchVideoFetch calls ensureNewAPIDeps() before reaching the adaptor;
	// a test driving FetchTask directly must do the same or the pool is nil.
	ensureNewAPIDeps()

	a := newXRTokenTaskAdaptor()
	resp, err := a.FetchTask(srv.URL, "tr-test-key", map[string]any{
		"task_id": "cgt-xr-fetch-888",
	}, "")
	if err != nil {
		t.Fatalf("FetchTask error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("FetchTask status = %d, want 200 (wrong path would 404 here)", resp.StatusCode)
	}
	if upstream.lastFetchPath != "/v1/contents/generations/tasks/cgt-xr-fetch-888" {
		t.Fatalf("fetch hit %q, want /v1/contents/generations/tasks/<id>", upstream.lastFetchPath)
	}
	if upstream.lastAuthHeader != "Bearer tr-test-key" {
		t.Fatalf("auth header = %q, want Bearer tr-test-key", upstream.lastAuthHeader)
	}

	// The inherited parser must still understand the (byte-identical) Ark
	// response body — that is the premise of wrapping instead of forking.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	info, err := a.ParseTaskResult(body)
	if err != nil {
		t.Fatalf("inherited ParseTaskResult failed on XRToken response: %v", err)
	}
	if info == nil || info.Status == "" {
		t.Fatalf("inherited ParseTaskResult returned no status: %+v", info)
	}
}

// TestXRTokenTaskAdaptor_FetchTask_Rejects guards the two fast-fail inputs.
func TestXRTokenTaskAdaptor_FetchTask_Rejects(t *testing.T) {
	t.Parallel()
	a := newXRTokenTaskAdaptor()
	if _, err := a.FetchTask(newapiintegration.XRTokenBaseURL, "k", map[string]any{}, ""); err == nil {
		t.Fatal("expected error for missing task_id, got nil")
	}
	if _, err := a.FetchTask("", "k", map[string]any{"task_id": "x"}, ""); err == nil {
		t.Fatal("expected error for empty base_url, got nil")
	}
}

// TestTaskAdaptorForChannel_XRTokenSelection pins the dispatch rule: the
// wrapper is selected ONLY for ch54 + the XRToken sentinel host. Every other
// combination must keep returning the unmodified upstream adaptor, so this
// override cannot leak onto official Ark accounts that share the same
// task-adaptor registration.
func TestTaskAdaptorForChannel_XRTokenSelection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		channelType int
		baseURL     string
		wantXR      bool
	}{
		{"xrtoken ch54 root", newapiconstant.ChannelTypeDoubaoVideo, newapiintegration.XRTokenBaseURL, true},
		{"xrtoken ch54 with /v1", newapiconstant.ChannelTypeDoubaoVideo, newapiintegration.XRTokenBaseURL + "/v1", true},
		{"official ark ch54", newapiconstant.ChannelTypeDoubaoVideo, "https://ark.cn-beijing.volces.com", false},
		{"xrtoken host on ch45", newapiconstant.ChannelTypeVolcEngine, newapiintegration.XRTokenBaseURL, false},
		{"empty base ch54", newapiconstant.ChannelTypeDoubaoVideo, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adaptor := taskAdaptorForChannel(tc.channelType, tc.baseURL)
			if adaptor == nil {
				t.Fatalf("taskAdaptorForChannel(%d, %q) = nil", tc.channelType, tc.baseURL)
			}
			_, isXR := adaptor.(*xrTokenTaskAdaptor)
			if isXR != tc.wantXR {
				t.Fatalf("taskAdaptorForChannel(%d, %q) xrtoken=%v, want %v",
					tc.channelType, tc.baseURL, isXR, tc.wantXR)
			}
		})
	}
}

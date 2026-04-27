//go:build unit

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// fakeUpstreamHandler is a Volcengine-shaped HTTP server that responds to
// POST /api/v3/contents/generations/tasks (submit) and
// GET /api/v3/contents/generations/tasks/:id (fetch). It captures the last
// payload so tests can assert the bridge passed the model + prompt through.
type fakeUpstreamHandler struct {
	lastSubmitBody []byte
	upstreamTaskID string
	fetchStatus    string
	fetchVideoURL  string
}

func (f *fakeUpstreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/v3/contents/generations/tasks"):
		body, _ := io.ReadAll(r.Body)
		f.lastSubmitBody = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"` + f.upstreamTaskID + `"}`))
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/api/v3/contents/generations/tasks/"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"` + f.upstreamTaskID + `","status":"` + f.fetchStatus + `","content":{"video_url":"` + f.fetchVideoURL + `"}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestDispatchVideoSubmit_VolcEngine_OK proves a Volcengine-shaped task
// adaptor can be driven through the bridge end-to-end (request marshalling,
// upstream POST, response parsing) WITHOUT touching new-api's billing or
// model.Task DB layer. This is the core regression check for "newapi fifth
// platform supports volcengine video generation".
func TestDispatchVideoSubmit_VolcEngine_OK(t *testing.T) {
	upstream := &fakeUpstreamHandler{upstreamTaskID: "cgt-volc-test-123"}
	srv := httptest.NewServer(upstream)
	defer srv.Close()

	body := mustJSON(t, map[string]any{
		"model":  "doubao-seedance-1-0-pro-250528",
		"prompt": "a cat playing piano",
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodPost, "/v1/video/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	in := ChannelContextInput{
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		ChannelID:   42,
		BaseURL:     srv.URL,
		APIKey:      "test-volc-key",
		UserID:      7,
	}
	const publicTaskID = "vt_test_123"
	out, apiErr := DispatchVideoSubmit(context.Background(), c, in, publicTaskID, body)
	if apiErr != nil {
		t.Fatalf("DispatchVideoSubmit returned error: %v", apiErr)
	}
	if out == nil {
		t.Fatal("expected outcome, got nil")
	}
	if out.PublicTaskID != publicTaskID {
		t.Fatalf("public task id not echoed: got %q want %q", out.PublicTaskID, publicTaskID)
	}
	if out.UpstreamTaskID != "cgt-volc-test-123" {
		t.Fatalf("upstream task id mismatch: %q", out.UpstreamTaskID)
	}
	if out.OriginModel != "doubao-seedance-1-0-pro-250528" {
		t.Fatalf("origin model not propagated: %q", out.OriginModel)
	}
	if !bytes.Contains(upstream.lastSubmitBody, []byte("doubao-seedance-1-0-pro-250528")) {
		t.Fatalf("upstream did not see the model field; body=%q", upstream.lastSubmitBody)
	}
	if !bytes.Contains(upstream.lastSubmitBody, []byte("a cat playing piano")) {
		t.Fatalf("upstream did not see the prompt; body=%q", upstream.lastSubmitBody)
	}
	// The new-api task adaptor's DoResponse writes the OpenAI-Video JSON
	// to the gin writer with relayInfo.PublicTaskID stamped as the id.
	// This asserts the bridge passes our publicTaskID through correctly
	// so the handler does NOT need to write a second c.JSON afterwards.
	if !bytes.Contains(w.Body.Bytes(), []byte(publicTaskID)) {
		t.Fatalf("response body missing publicTaskID stamp; body=%q", w.Body.Bytes())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("cgt-volc-test-123")) {
		t.Fatalf("response body MUST NOT leak upstream task id; body=%q", w.Body.Bytes())
	}
}

// TestDispatchVideoSubmit_RejectsEmptyPublicTaskID locks in the design
// invariant that the caller must pre-generate the public task id; without
// it the adaptor would stamp an empty string into the response body and
// the GET /v1/videos/:task_id alias would have no key to look up.
func TestDispatchVideoSubmit_RejectsEmptyPublicTaskID(t *testing.T) {
	body := mustJSON(t, map[string]any{"model": "x", "prompt": "x"})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	in := ChannelContextInput{ChannelType: newapiconstant.ChannelTypeVolcEngine, BaseURL: "http://nowhere", APIKey: "k"}
	if _, err := DispatchVideoSubmit(context.Background(), c, in, "  ", body); err == nil {
		t.Fatal("expected error for empty public_task_id, got nil")
	}
}

// TestDispatchVideoSubmit_RejectsUnknownChannelType asserts that the bridge
// returns a typed error rather than nil when no task adaptor is registered.
// This protects the gateway from silently 5xx-ing if an admin assigns
// channel_type=0 to a newapi account that ought to do video.
func TestDispatchVideoSubmit_RejectsUnknownChannelType(t *testing.T) {
	body := mustJSON(t, map[string]any{
		"model":  "anything",
		"prompt": "x",
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	in := ChannelContextInput{ChannelType: 9999, BaseURL: "http://x", APIKey: "k"}
	if _, err := DispatchVideoSubmit(context.Background(), c, in, "vt_x", body); err == nil {
		t.Fatal("expected error for unsupported channel_type, got nil")
	}
}

// TestDispatchVideoSubmit_MissingModel asserts the bridge fast-fails on
// invalid request shape before opening any upstream connection.
func TestDispatchVideoSubmit_MissingModel(t *testing.T) {
	body := []byte(`{"prompt":"x"}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	in := ChannelContextInput{ChannelType: newapiconstant.ChannelTypeVolcEngine, BaseURL: "http://nowhere", APIKey: "k"}
	if _, err := DispatchVideoSubmit(context.Background(), c, in, "vt_x", body); err == nil {
		t.Fatal("expected error for missing model, got nil")
	}
}

// TestDispatchVideoFetch_VolcEngine_OK exercises the polling path: given a
// known upstream task id + channel + base url, the bridge should call FetchTask
// and return the upstream raw response plus a parsed status snapshot.
func TestDispatchVideoFetch_VolcEngine_OK(t *testing.T) {
	upstream := &fakeUpstreamHandler{
		upstreamTaskID: "cgt-volc-test-123",
		fetchStatus:    "succeeded",
		fetchVideoURL:  "https://cdn.example.com/video.mp4",
	}
	srv := httptest.NewServer(upstream)
	defer srv.Close()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/cgt-volc-test-123", nil)

	in := VideoFetchInput{
		UpstreamTaskID: "cgt-volc-test-123",
		ChannelType:    newapiconstant.ChannelTypeVolcEngine,
		BaseURL:        srv.URL,
		APIKey:         "test-volc-key",
	}
	out, apiErr := DispatchVideoFetch(context.Background(), c, in)
	if apiErr != nil {
		t.Fatalf("DispatchVideoFetch returned error: %v", apiErr)
	}
	if out == nil {
		t.Fatal("expected outcome, got nil")
	}
	if out.Status == "" {
		t.Fatalf("expected non-empty status, got %q", out.Status)
	}
	// RawResponse passes the upstream JSON through untouched so the SDK sees
	// the same body shape it would from new-api directly. The URL/progress
	// extraction is done client-side from this raw payload.
	if !bytes.Contains(out.RawResponse, []byte("https://cdn.example.com/video.mp4")) {
		t.Fatalf("raw response missing video url, got %q", out.RawResponse)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

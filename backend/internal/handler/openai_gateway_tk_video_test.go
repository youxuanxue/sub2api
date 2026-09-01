//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapitypes "github.com/QuantumNous/new-api/types"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestVideoFetch_CrossUser_Returns404 enforces the per-user authorization
// invariant on /v1/video/generations/:task_id (and the OpenAI-compat alias
// /v1/videos/:task_id). The route layer's compat-platform gate is necessary
// but NOT sufficient — without this handler-level user_id match, any
// authenticated user with an OpenAI-compat group could poll any task by
// guessing or replaying a `vt_*` id, leaking the upstream JSON (which on
// success includes the generated video URL).
//
// We deliberately return 404 (not 403) to avoid confirming the task_id
// exists for another user. The record itself is left intact — only this
// caller lost the lookup.
func TestVideoFetch_CrossUser_Returns404(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := repository.NewVideoTaskCache(nil)
	if err := cache.Save(context.Background(), &service.VideoTaskRecord{
		PublicTaskID:   "vt_owned_by_user_one",
		UpstreamTaskID: "cgt-owner-task",
		UserID:         1, // task owner
		ChannelType:    45,
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	h := &OpenAIGatewayHandler{}
	h.SetVideoTaskCache(cache)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/vt_owned_by_user_one", nil)
	c.Params = gin.Params{{Key: "task_id", Value: "vt_owned_by_user_one"}}

	// User 2 is authenticated but does NOT own the task. Mirrors what
	// ApiKeyAuth middleware would set on a real request.
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 2})

	h.VideoFetch(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-user fetch, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not json: %v body=%s", err, w.Body.String())
	}
	if body.Error.Type != "not_found_error" {
		t.Fatalf("expected not_found_error, got %q", body.Error.Type)
	}

	// The record MUST still be in the registry — a foreign GET should not
	// expire someone else's task.
	if _, ok := cache.Lookup(context.Background(), "vt_owned_by_user_one"); !ok {
		t.Fatal("foreign 404 must not delete the record")
	}
}

// TestVideoFetch_NilRegistry_Returns503 verifies the nil-safety contract on
// SetVideoTaskCache: a handler constructed by an older Wire path that
// does not yet wire the registry (e.g. mid-rollout) MUST 503, not panic.
func TestVideoFetch_NilRegistry_Returns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpenAIGatewayHandler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/vt_x", nil)
	c.Params = gin.Params{{Key: "task_id", Value: "vt_x"}}
	h.VideoFetch(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

// TestVideoFetch_MissingTaskID_Returns400 covers the trivial case so the
// fetch handler does not silently route an empty task_id to a registry
// scan that returns nil.
func TestVideoFetch_MissingTaskID_Returns400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cache := repository.NewVideoTaskCache(nil)
	h := &OpenAIGatewayHandler{}
	h.SetVideoTaskCache(cache)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/", nil)
	// No params set → c.Param("task_id") == ""
	h.VideoFetch(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestVideoFetch_FailedTerminalDeletesTaskAndSchedulesRefund(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/api/v3/contents/generations/tasks/upstream-failed") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"upstream-failed","status":"failed","error":{"message":"generation failed"}}`))
	}))
	defer upstream.Close()

	cache := repository.NewVideoTaskCache(nil)
	record := &service.VideoTaskRecord{
		PublicTaskID:   "vt_failed",
		UpstreamTaskID: "upstream-failed",
		UserID:         1,
		ChannelType:    newapiconstant.ChannelTypeVolcEngine,
		BaseURL:        upstream.URL,
		APIKey:         "test-key",
	}
	if err := cache.Save(context.Background(), record); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	gatewayService := service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &OpenAIGatewayHandler{gatewayService: gatewayService}
	h.SetVideoTaskCache(cache)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/vt_failed", nil)
	c.Params = gin.Params{{Key: "task_id", Value: "vt_failed"}}
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})

	h.VideoFetch(c)

	if w.Code != http.StatusOK {
		t.Fatalf("failed terminal status should be returned to the owner, got %d body=%s", w.Code, w.Body.String())
	}
	if _, ok := cache.Lookup(context.Background(), "vt_failed"); ok {
		t.Fatal("failed terminal task must be deleted from the registry")
	}
	require.True(t, logSink.ContainsMessageAtLevel("openai_video_refund.skipped_no_billing_request_id", "warn"),
		"failed terminal fetch must schedule the refund path")
}

func TestVideoFetch_RequestIDAliasReadsTokenKeyTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cache := repository.NewVideoTaskCache(nil)
	if err := cache.Save(context.Background(), &service.VideoTaskRecord{
		PublicTaskID: "vt_alias_owned",
		UserID:       1,
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	h := &OpenAIGatewayHandler{}
	h.SetVideoTaskCache(cache)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/generations/vt_alias_owned", nil)
	c.Params = gin.Params{{Key: "request_id", Value: "vt_alias_owned"}}
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 2})

	h.VideoFetch(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("request_id alias must reach the same ownership check, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestVideoDurationBelowProviderMinimum(t *testing.T) {
	xrToken := &service.Account{
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeDoubaoVideo,
		Credentials: map[string]any{"base_url": newapiintegration.XRTokenBaseURL},
	}
	nonXRToken := &service.Account{
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeDoubaoVideo,
		Credentials: map[string]any{"base_url": "https://ark.cn-beijing.volces.com"},
	}

	if !videoDurationBelowProviderMinimum(xrToken, []byte(`{"seconds":1}`)) {
		t.Fatal("XRToken one-second request must be rejected before dispatch")
	}
	if videoDurationBelowProviderMinimum(xrToken, []byte(`{"seconds":4}`)) {
		t.Fatal("XRToken four-second request must satisfy local validation")
	}
	if videoDurationBelowProviderMinimum(nonXRToken, []byte(`{"seconds":1}`)) {
		t.Fatal("provider-specific minimum must not affect non-XRToken accounts")
	}
}

func TestTryWriteVideoRelayErrorUsesUnwrittenWriterBaseline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	before := c.Writer.Size()
	err := &service.NewAPIRelayError{Err: newapitypes.NewErrorWithStatusCode(
		http.ErrAbortHandler,
		newapitypes.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		newapitypes.ErrOptionWithSkipRetry(),
	)}

	if !TkTryWriteNewAPIRelayErrorJSON(c, err, false, before) {
		t.Fatal("relay error must be recognized")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("upstream 400 must render as client 400, got %d body=%s", w.Code, w.Body.String())
	}
	if w.Body.Len() == 0 {
		t.Fatal("upstream 400 must render a JSON error body")
	}
}

func TestVideoRequestedSeconds(t *testing.T) {
	cases := []struct {
		body string
		want int64
	}{
		{`{"model":"veo-3.1-generate-preview","prompt":"x"}`, 8}, // default
		{`{"seconds":4}`, 4},          // numeric
		{`{"seconds":"6"}`, 6},        // string numeric
		{`{"duration_seconds":5}`, 5}, // alt key
		{`{"duration":3}`, 3},         // alt key
		{`{"seconds":120}`, 60},       // clamp upper
		{`{"seconds":0}`, 8},          // non-positive → default
		{`{"seconds":4.6}`, 5},        // float rounds
	}
	for _, c := range cases {
		if got := videoRequestedSeconds([]byte(c.body)); got != c.want {
			t.Errorf("videoRequestedSeconds(%s)=%d want %d", c.body, got, c.want)
		}
	}
}

func TestVideoBillingSecondsForAccount(t *testing.T) {
	fmgo := &service.Account{
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeDoubaoVideo,
		Credentials: map[string]any{"base_url": newapiintegration.FMGoBaseURL},
	}
	ark := &service.Account{
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeDoubaoVideo,
		Credentials: map[string]any{"base_url": "https://ark.cn-beijing.volces.com"},
	}

	require.Equal(t, int64(newapiintegration.FMGoDefaultDuration), videoBillingSecondsForAccount(fmgo, []byte(`{"model":"doubao-seedance-2-0-260128"}`)))
	require.Equal(t, int64(8), videoBillingSecondsForAccount(ark, []byte(`{"model":"doubao-seedance-2-0-260128"}`)))
	require.Equal(t, int64(6), videoBillingSecondsForAccount(fmgo, []byte(`{"duration":6}`)))
	require.Equal(t, int64(newapiintegration.FMGoDefaultDuration), videoBillingSecondsForAccount(fmgo, []byte(`{"duration":0}`)))
}

func TestVideoSubmitHasVideoInput(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"no content", `{"model":"m","prompt":"x","seconds":5}`, false},
		{"text only", `{"content":[{"type":"text","text":"hi"}]}`, false},
		{"first-frame image passes", `{"content":[{"type":"image_url","image_url":{"url":"https://a/b.png"}}]}`, false},
		{"video_url type", `{"content":[{"type":"video_url","video_url":{"url":"https://a/b.mp4"}}]}`, true},
		{"video_url key without type", `{"content":[{"video_url":{"url":"https://a/b.mp4"}}]}`, true},
		{"mixed image then video", `{"content":[{"type":"image_url","image_url":{"url":"u"}},{"type":"video_url","video_url":{"url":"v"}}]}`, true},
		{"pre-nested metadata.content", `{"metadata":{"content":[{"type":"video_url","video_url":{"url":"v"}}]}}`, true},
		{"stringified metadata with video (new-api accepts metadata as JSON string)", `{"metadata":"{\"content\":[{\"type\":\"video_url\",\"video_url\":{\"url\":\"v\"}}]}"}`, true},
		{"stringified metadata image only passes", `{"metadata":"{\"content\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"u\"}}]}"}`, false},
		{"stringified metadata not json", `{"metadata":"not json at all"}`, false},
		{"content not an array", `{"content":"a string"}`, false},
	}
	for _, c := range cases {
		if got := videoSubmitHasVideoInput([]byte(c.body)); got != c.want {
			t.Errorf("%s: videoSubmitHasVideoInput=%v want %v", c.name, got, c.want)
		}
	}
}

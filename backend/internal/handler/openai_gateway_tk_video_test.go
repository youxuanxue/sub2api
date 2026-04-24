//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
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

	registry := service.NewVideoTaskRegistry(nil)
	if err := registry.Save(context.Background(), &service.VideoTaskRecord{
		PublicTaskID:   "vt_owned_by_user_one",
		UpstreamTaskID: "cgt-owner-task",
		UserID:         1, // task owner
		ChannelType:    45,
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	h := &OpenAIGatewayHandler{}
	h.SetVideoTaskRegistry(registry)

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
	if _, ok := registry.Lookup(context.Background(), "vt_owned_by_user_one"); !ok {
		t.Fatal("foreign 404 must not delete the record")
	}
}

// TestVideoFetch_NilRegistry_Returns503 verifies the nil-safety contract on
// SetVideoTaskRegistry: a handler constructed by an older Wire path that
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
	registry := service.NewVideoTaskRegistry(nil)
	h := &OpenAIGatewayHandler{}
	h.SetVideoTaskRegistry(registry)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/video/generations/", nil)
	// No params set → c.Param("task_id") == ""
	h.VideoFetch(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

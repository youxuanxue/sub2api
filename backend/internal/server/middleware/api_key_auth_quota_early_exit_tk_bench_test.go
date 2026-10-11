//go:build unit

package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// BenchmarkAPIKeyQuotaRejection measures the per-rejection cost of the hoisted
// path. Compare against the same benchmark with the early exit disabled to see
// the work avoided; the gap is the body read, the OpenRouter peek and the JSON
// parse. Candidate scheduling is not exercised here (this harness has no
// backing span store), so the real production saving is strictly larger.
func BenchmarkAPIKeyQuotaRejection(b *testing.B) {
	gin.SetMode(gin.TestMode)

	user := &service.User{ID: 16, Role: service.RoleUser, Status: service.StatusActive, Balance: 10}
	group := &service.Group{ID: 8, Platform: service.PlatformAnthropic, Status: service.StatusActive, Hydrated: true}
	apiKey := &service.APIKey{
		ID: 362, UserID: user.ID, Key: "quota-exhausted-bench",
		Status: service.StatusAPIKeyQuotaExhausted,
		User:   user, Group: group, GroupID: &group.ID,
		Quota: 500, QuotaUsed: 500.04,
	}
	repo := &stubApiKeyRepo{getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
		clone := *apiKey
		userClone := *user
		clone.User = &userClone
		return &clone, nil
	}}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	router := quotaEarlyExitRouter(svc, cfg)

	// A realistic conversational payload: the storm's rejected requests carried
	// full message histories, each of which was read and parsed.
	payload := []byte(`{"model":"claude-sonnet-4-5","max_tokens":4096,"messages":[` +
		`{"role":"user","content":"` + string(bytes.Repeat([]byte("analyse this log line. "), 400)) + `"},` +
		`{"role":"assistant","content":"` + string(bytes.Repeat([]byte("here is the analysis. "), 400)) + `"},` +
		`{"role":"user","content":"continue"}]}`)

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(payload))
		req.Header.Set("x-api-key", apiKey.Key)
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		if w.Code != http.StatusTooManyRequests {
			b.Fatalf("expected 429, got %d", w.Code)
		}
	}
}

//go:build unit

package qa

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestUserQACaptureEnabled_PersistsWhenFlagTrue locks the positive path:
// site-wide capture on + user.qa_capture_enabled=true → qa_records row written.
func TestUserQACaptureEnabled_PersistsWhenFlagTrue(t *testing.T) {
	svc, client, _ := newQACaptureTestService(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
			ID:     5,
			UserID: 7,
			User:   &service.User{ID: 7, QACaptureEnabled: true},
			Group:  &service.Group{Platform: service.PlatformAnthropic},
		})
		c.Next()
	})
	r.Use(svc.Middleware())
	r.POST("/v1/messages", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "qa-user-gate-on")
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	require.Eventually(t, func() bool {
		n, err := client.QARecord.Query().Count(context.Background())
		return err == nil && n == 1
	}, 2*time.Second, 10*time.Millisecond)
}

// TestUserQACaptureEnabled_SkipsWhenFlagFalse locks the negative path:
// site-wide capture on + user.qa_capture_enabled=false → no qa_records / no blobs.
func TestUserQACaptureEnabled_SkipsWhenFlagFalse(t *testing.T) {
	svc, client, store := newQACaptureTestService(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
			ID:     5,
			UserID: 7,
			User:   &service.User{ID: 7, QACaptureEnabled: false},
			Group:  &service.Group{Platform: service.PlatformAnthropic},
		})
		c.Next()
	})
	r.Use(svc.Middleware())
	r.POST("/v1/messages", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true, "secret": "must-not-be-captured"})
	})

	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "qa-user-gate-off")
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","prompt":"private"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code)

	// Give the async pool a moment; then assert nothing was persisted.
	time.Sleep(100 * time.Millisecond)
	n, err := client.QARecord.Query().Count(context.Background())
	require.NoError(t, err)
	require.Zero(t, n, "disabled user must not create qa_records")
	require.Empty(t, store.objects, "disabled user must not write QA blobs")
}

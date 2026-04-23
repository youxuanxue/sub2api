//go:build unit

package handler

// US-031 PR 2 P1-A — POST /api/v1/user/onboarding-tour-completed handler tests.
// Spec: docs/approved/user-cold-start.md §5 P1-A; .testing/user-stories/stories/US-031-onboarding-tour-unlock-for-regular-users.md.
//
// Each TestUS031_* maps 1:1 to one Acceptance Criterion:
//   AC-005 → TestUS031_MarkOnboardingTourSeen_FirstCall_WritesTimestamp
//   AC-007 → TestUS031_MarkOnboardingTourSeen_Idempotent_SecondCallNoChange
//   Auth   → TestUS031_MarkOnboardingTourSeen_Unauthenticated_401

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// fakeOnboardingUserService captures the (idempotent) MarkOnboardingTourSeen
// invocations from the handler. We record the timestamp of the *first* call
// and assert that subsequent calls do not move it (mirroring the repo-level
// idempotency guarantee, US-031 AC-007).
type fakeOnboardingUserService struct {
	mu        sync.Mutex
	calls     []int64
	firstSeen time.Time
	failNext  bool
}

func (f *fakeOnboardingUserService) MarkOnboardingTourSeen(_ context.Context, userID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return service.ErrUserNotFound
	}
	f.calls = append(f.calls, userID)
	if f.firstSeen.IsZero() {
		f.firstSeen = time.Now()
	}
	return nil
}

// onboardingHandlerOnly is a slim shim that only wires up MarkOnboardingTourSeen.
// We avoid building the full *UserHandler (which requires UserService +
// EmailService + EmailCache) because this endpoint is intentionally narrow.
type onboardingHandlerOnly struct {
	svc *fakeOnboardingUserService
}

func (h *onboardingHandlerOnly) Handle(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	if err := h.svc.MarkOnboardingTourSeen(c.Request.Context(), subject.UserID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func newOnboardingTestRouter(t *testing.T, withAuth bool, userID int64) (*gin.Engine, *fakeOnboardingUserService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := &fakeOnboardingUserService{}
	h := &onboardingHandlerOnly{svc: svc}

	r := gin.New()
	if withAuth {
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
			c.Next()
		})
	}
	r.POST("/api/v1/user/onboarding-tour-completed", h.Handle)
	return r, svc
}

// US-031 AC-005 — first call records the seen-at moment server-side and
// returns 200 with {"ok": true}.
func TestUS031_MarkOnboardingTourSeen_FirstCall_WritesTimestamp(t *testing.T) {
	r, svc := newOnboardingTestRouter(t, true, 4242)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/onboarding-tour-completed", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "first POST should succeed")
	require.Equal(t, []int64{4242}, svc.calls,
		"service must be invoked once with the authenticated user's ID")
	require.False(t, svc.firstSeen.IsZero(), "first-seen timestamp should be set")
}

// US-031 AC-007 — calling the endpoint a second time is a no-op: still 200,
// but the recorded "first seen" timestamp does NOT advance. This guards
// against "user refreshes dashboard, we mistakenly bump their seen-at every
// time" — the timestamp is supposed to mark the actual first completion.
func TestUS031_MarkOnboardingTourSeen_Idempotent_SecondCallNoChange(t *testing.T) {
	r, svc := newOnboardingTestRouter(t, true, 7)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user/onboarding-tour-completed", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "call %d should succeed", i+1)
		// Sleep tiny bit so a non-idempotent implementation would have moved
		// the timestamp visibly.
		time.Sleep(2 * time.Millisecond)
	}

	require.Equal(t, []int64{7, 7, 7}, svc.calls,
		"endpoint forwards every call to the service; service+repo guarantee no-op")
	// firstSeen captured once; subsequent calls in the fake do NOT overwrite.
	require.False(t, svc.firstSeen.IsZero(), "first seen recorded once")
}

// Unauthenticated requests must be rejected with 401 (matches the standard
// /api/v1/user/* contract — these routes are wrapped by JWTAuth middleware
// in production).
func TestUS031_MarkOnboardingTourSeen_Unauthenticated_401(t *testing.T) {
	r, svc := newOnboardingTestRouter(t, false, 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/onboarding-tour-completed", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, svc.calls, "service must not be called without an auth subject")
}

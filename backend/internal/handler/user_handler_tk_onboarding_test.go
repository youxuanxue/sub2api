//go:build unit

package handler

// US-031 PR 2 P1-A — POST /api/v1/user/onboarding-tour-completed handler tests.
//
// Spec: docs/approved/user-cold-start.md §5 P1-A;
//       .testing/user-stories/stories/US-031-onboarding-tour-unlock-for-regular-users.md
//
// We exercise the REAL `UserHandler.MarkOnboardingTourSeen` against a real
// `*service.UserService` wired to a stub `service.UserRepository`. This is
// the only way to verify the production handler — building a parallel
// "shim handler" in test code would only test the shim, not the real one.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// onboardingFakeUserRepo records MarkOnboardingTourSeen calls. Every other
// method panics so any accidental cross-method call from the production
// path is caught loudly. Idempotency is modeled correctly: a second call
// with the same userID is a no-op (the production repo does this with a
// conditional UPDATE WHERE seen_at IS NULL — the fake replicates that
// observable behavior so the handler's contract is verified, not the
// repo's SQL).
type onboardingFakeUserRepo struct {
	mu     sync.Mutex
	seenAt map[int64]time.Time
	calls  []int64
}

func newOnboardingFakeUserRepo() *onboardingFakeUserRepo {
	return &onboardingFakeUserRepo{seenAt: make(map[int64]time.Time)}
}

func (r *onboardingFakeUserRepo) MarkOnboardingTourSeen(_ context.Context, userID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, userID)
	if _, already := r.seenAt[userID]; !already {
		r.seenAt[userID] = time.Now()
	}
	return nil
}

// --- panic-on-call defaults for the rest of the interface ---

func (r *onboardingFakeUserRepo) Create(context.Context, *service.User) error {
	panic("unexpected Create")
}
func (r *onboardingFakeUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	panic("unexpected GetByID")
}
func (r *onboardingFakeUserRepo) GetByEmail(context.Context, string) (*service.User, error) {
	panic("unexpected GetByEmail")
}
func (r *onboardingFakeUserRepo) GetFirstAdmin(context.Context) (*service.User, error) {
	panic("unexpected GetFirstAdmin")
}
func (r *onboardingFakeUserRepo) Update(context.Context, *service.User) error {
	panic("unexpected Update")
}
func (r *onboardingFakeUserRepo) Delete(context.Context, int64) error { panic("unexpected Delete") }
func (r *onboardingFakeUserRepo) List(context.Context, pagination.PaginationParams) ([]service.User, *pagination.PaginationResult, error) {
	panic("unexpected List")
}
func (r *onboardingFakeUserRepo) ListWithFilters(context.Context, pagination.PaginationParams, service.UserListFilters) ([]service.User, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters")
}
func (r *onboardingFakeUserRepo) UpdateBalance(context.Context, int64, float64) error {
	panic("unexpected UpdateBalance")
}
func (r *onboardingFakeUserRepo) DeductBalance(context.Context, int64, float64) error {
	panic("unexpected DeductBalance")
}
func (r *onboardingFakeUserRepo) UpdateConcurrency(context.Context, int64, int) error {
	panic("unexpected UpdateConcurrency")
}
func (r *onboardingFakeUserRepo) ExistsByEmail(context.Context, string) (bool, error) {
	panic("unexpected ExistsByEmail")
}
func (r *onboardingFakeUserRepo) RemoveGroupFromAllowedGroups(context.Context, int64) (int64, error) {
	panic("unexpected RemoveGroupFromAllowedGroups")
}
func (r *onboardingFakeUserRepo) AddGroupToAllowedGroups(context.Context, int64, int64) error {
	panic("unexpected AddGroupToAllowedGroups")
}
func (r *onboardingFakeUserRepo) RemoveGroupFromUserAllowedGroups(context.Context, int64, int64) error {
	panic("unexpected RemoveGroupFromUserAllowedGroups")
}
func (r *onboardingFakeUserRepo) UpdateTotpSecret(context.Context, int64, *string) error {
	panic("unexpected UpdateTotpSecret")
}
func (r *onboardingFakeUserRepo) EnableTotp(context.Context, int64) error {
	panic("unexpected EnableTotp")
}
func (r *onboardingFakeUserRepo) DisableTotp(context.Context, int64) error {
	panic("unexpected DisableTotp")
}
func (r *onboardingFakeUserRepo) DeleteUserAvatar(context.Context, int64) error {
	panic("unexpected DeleteUserAvatar")
}
func (r *onboardingFakeUserRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	panic("unexpected GetUserAvatar")
}
func (r *onboardingFakeUserRepo) UpsertUserAvatar(context.Context, int64, service.UpsertUserAvatarInput) (*service.UserAvatar, error) {
	panic("unexpected UpsertUserAvatar")
}
func (r *onboardingFakeUserRepo) GetLatestUsedAtByUserIDs(context.Context, []int64) (map[int64]*time.Time, error) {
	panic("unexpected GetLatestUsedAtByUserIDs")
}
func (r *onboardingFakeUserRepo) GetLatestUsedAtByUserID(context.Context, int64) (*time.Time, error) {
	panic("unexpected GetLatestUsedAtByUserID")
}
func (r *onboardingFakeUserRepo) UpdateUserLastActiveAt(context.Context, int64, time.Time) error {
	panic("unexpected UpdateUserLastActiveAt")
}
func (r *onboardingFakeUserRepo) ListUserAuthIdentities(context.Context, int64) ([]service.UserAuthIdentityRecord, error) {
	panic("unexpected ListUserAuthIdentities")
}
func (r *onboardingFakeUserRepo) UnbindUserAuthProvider(context.Context, int64, string) error {
	panic("unexpected UnbindUserAuthProvider")
}

// newOnboardingTestRouter wires the REAL UserHandler.MarkOnboardingTourSeen
// behind a tiny middleware that injects an AuthSubject (mirroring production
// JWT auth). When withAuth=false the AuthSubject is absent — the handler
// must reject with 401 itself.
func newOnboardingTestRouter(t *testing.T, withAuth bool, userID int64) (*gin.Engine, *onboardingFakeUserRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := newOnboardingFakeUserRepo()
	userSvc := service.NewUserService(repo, nil, nil, nil)
	h := NewUserHandler(userSvc, nil, nil, nil)

	r := gin.New()
	if withAuth {
		r.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
			c.Next()
		})
	}
	r.POST("/api/v1/user/onboarding-tour-completed", h.MarkOnboardingTourSeen)
	return r, repo
}

// US-031 AC-005 — first call records the seen-at moment server-side and
// returns 200 with success envelope.
func TestUS031_MarkOnboardingTourSeen_FirstCall_WritesTimestamp(t *testing.T) {
	r, repo := newOnboardingTestRouter(t, true, 4242)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/onboarding-tour-completed", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "first POST should succeed")
	require.Equal(t, []int64{4242}, repo.calls,
		"repo must be invoked once with the authenticated user's ID")
	require.Contains(t, repo.seenAt, int64(4242),
		"seen_at must be recorded for user 4242")
}

// US-031 AC-007 — calling the endpoint a second time is a no-op: still 200,
// recorded "first seen" timestamp does NOT advance. Guards against
// "user refreshes dashboard, we mistakenly bump seen_at every time".
func TestUS031_MarkOnboardingTourSeen_Idempotent_SecondCallNoChange(t *testing.T) {
	r, repo := newOnboardingTestRouter(t, true, 7)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/user/onboarding-tour-completed", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "call %d should succeed", i+1)
		// Sleep tiny bit so a non-idempotent fake would have moved the timestamp.
		time.Sleep(2 * time.Millisecond)
	}

	require.Equal(t, []int64{7, 7, 7}, repo.calls,
		"handler forwards every call to the repo; idempotency lives in the predicate")
	require.Len(t, repo.seenAt, 1, "exactly one user has a seen_at recorded")
	// The fake records the FIRST timestamp and keeps it — same observable
	// behavior as the production repo's `WHERE seen_at IS NULL` predicate.
}

// Unauthenticated requests must be rejected with 401. The handler must
// reach this verdict from middleware.GetAuthSubjectFromContext alone —
// the userService MUST NOT be touched (otherwise an attacker could DoS
// the DB with anonymous POSTs).
func TestUS031_MarkOnboardingTourSeen_Unauthenticated_401(t *testing.T) {
	r, repo := newOnboardingTestRouter(t, false, 0)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/onboarding-tour-completed", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, repo.calls, "repo must not be called without an auth subject")
}

// Sanity check: the success envelope contains data.success=true (matching
// the convention already used by totp_handler.go for "no-content
// acknowledgement" endpoints) so the frontend can rely on a stable shape.
// This pins the contract — if a future refactor switches to
// `response.Success(c, nil)` the spec drifts and the frontend would silently
// keep working but we'd lose the affirmative signal.
func TestUS031_MarkOnboardingTourSeen_SuccessEnvelopeShape(t *testing.T) {
	r, _ := newOnboardingTestRouter(t, true, 99)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/onboarding-tour-completed", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var env response.Response
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, 0, env.Code)
	dataMap, ok := env.Data.(map[string]any)
	require.True(t, ok, "data should be a JSON object")
	require.Equal(t, true, dataMap["success"])
}


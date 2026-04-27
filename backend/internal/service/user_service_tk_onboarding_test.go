//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

// Minimal stub focused on MarkOnboardingTourSeen — every other method panics
// so accidental usage from another path is caught loudly.
type onboardingUserRepoStub struct {
	calls     []int64
	returnErr error
}

func (s *onboardingUserRepoStub) MarkOnboardingTourSeen(_ context.Context, userID int64) error {
	s.calls = append(s.calls, userID)
	return s.returnErr
}

// --- panic-on-call defaults for the rest of the interface ---

func (s *onboardingUserRepoStub) Create(context.Context, *User) error { panic("unexpected Create") }
func (s *onboardingUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	panic("unexpected GetByID")
}
func (s *onboardingUserRepoStub) GetByEmail(context.Context, string) (*User, error) {
	panic("unexpected GetByEmail")
}
func (s *onboardingUserRepoStub) GetFirstAdmin(context.Context) (*User, error) {
	panic("unexpected GetFirstAdmin")
}
func (s *onboardingUserRepoStub) Update(context.Context, *User) error { panic("unexpected Update") }
func (s *onboardingUserRepoStub) Delete(context.Context, int64) error { panic("unexpected Delete") }
func (s *onboardingUserRepoStub) List(context.Context, pagination.PaginationParams) ([]User, *pagination.PaginationResult, error) {
	panic("unexpected List")
}
func (s *onboardingUserRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, UserListFilters) ([]User, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters")
}
func (s *onboardingUserRepoStub) UpdateBalance(context.Context, int64, float64) error {
	panic("unexpected UpdateBalance")
}
func (s *onboardingUserRepoStub) DeductBalance(context.Context, int64, float64) error {
	panic("unexpected DeductBalance")
}
func (s *onboardingUserRepoStub) UpdateConcurrency(context.Context, int64, int) error {
	panic("unexpected UpdateConcurrency")
}
func (s *onboardingUserRepoStub) ExistsByEmail(context.Context, string) (bool, error) {
	panic("unexpected ExistsByEmail")
}
func (s *onboardingUserRepoStub) RemoveGroupFromAllowedGroups(context.Context, int64) (int64, error) {
	panic("unexpected RemoveGroupFromAllowedGroups")
}
func (s *onboardingUserRepoStub) AddGroupToAllowedGroups(context.Context, int64, int64) error {
	panic("unexpected AddGroupToAllowedGroups")
}
func (s *onboardingUserRepoStub) RemoveGroupFromUserAllowedGroups(context.Context, int64, int64) error {
	panic("unexpected RemoveGroupFromUserAllowedGroups")
}
func (s *onboardingUserRepoStub) UpdateTotpSecret(context.Context, int64, *string) error {
	panic("unexpected UpdateTotpSecret")
}
func (s *onboardingUserRepoStub) EnableTotp(context.Context, int64) error {
	panic("unexpected EnableTotp")
}
func (s *onboardingUserRepoStub) DisableTotp(context.Context, int64) error {
	panic("unexpected DisableTotp")
}
func (s *onboardingUserRepoStub) GetUserAvatar(context.Context, int64) (*UserAvatar, error) {
	panic("unexpected GetUserAvatar")
}
func (s *onboardingUserRepoStub) UpsertUserAvatar(context.Context, int64, UpsertUserAvatarInput) (*UserAvatar, error) {
	panic("unexpected UpsertUserAvatar")
}
func (s *onboardingUserRepoStub) DeleteUserAvatar(context.Context, int64) error {
	panic("unexpected DeleteUserAvatar")
}
func (s *onboardingUserRepoStub) GetLatestUsedAtByUserIDs(context.Context, []int64) (map[int64]*time.Time, error) {
	panic("unexpected GetLatestUsedAtByUserIDs")
}
func (s *onboardingUserRepoStub) GetLatestUsedAtByUserID(context.Context, int64) (*time.Time, error) {
	panic("unexpected GetLatestUsedAtByUserID")
}
func (s *onboardingUserRepoStub) UpdateUserLastActiveAt(context.Context, int64, time.Time) error {
	panic("unexpected UpdateUserLastActiveAt")
}
func (s *onboardingUserRepoStub) ListUserAuthIdentities(context.Context, int64) ([]UserAuthIdentityRecord, error) {
	panic("unexpected ListUserAuthIdentities")
}
func (s *onboardingUserRepoStub) UnbindUserAuthProvider(context.Context, int64, string) error {
	panic("unexpected UnbindUserAuthProvider")
}

// US-031 AC-005 + service-layer wiring contract:
// UserService.MarkOnboardingTourSeen MUST delegate to UserRepository.MarkOnboardingTourSeen
// with the same userID, and return its error wrapped (so callers see a typed
// chain rather than a bare repo error).
func TestUS031_MarkOnboardingTourSeen_DelegatesToRepo(t *testing.T) {
	stub := &onboardingUserRepoStub{}
	svc := NewUserService(stub, nil, nil, nil)

	err := svc.MarkOnboardingTourSeen(context.Background(), 4242)
	require.NoError(t, err)
	require.Equal(t, []int64{4242}, stub.calls,
		"service must forward the same userID to the repo")
}

// US-031 AC-007 idempotency contract is enforced at the *repository* layer
// (a conditional UPDATE ... WHERE onboarding_tour_seen_at IS NULL); the
// service is intentionally a thin pass-through. This test guards against a
// future refactor that adds a "GetByID then conditionally call repo"
// pre-check at the service layer (which would break idempotency under
// concurrent requests). We assert that the service does NOT consult GetByID.
func TestUS031_MarkOnboardingTourSeen_AlreadySeen_NoUpdate(t *testing.T) {
	stub := &onboardingUserRepoStub{}
	svc := NewUserService(stub, nil, nil, nil)

	require.NoError(t, svc.MarkOnboardingTourSeen(context.Background(), 1))
	require.NoError(t, svc.MarkOnboardingTourSeen(context.Background(), 1))
	// Repo got both calls; idempotency lives in repo (the UPDATE is a no-op
	// the second time because the WHERE clause filters it out). Service must
	// not pre-check via GetByID — that would be an unexpected GetByID panic.
	require.Equal(t, []int64{1, 1}, stub.calls)
}

// US-031 — repo error propagation: when repo fails (DB outage etc.), the
// service must return a wrapped error so callers can react (the handler
// then logs + returns 500). Best-effort UX is handled in the handler layer.
func TestUS031_MarkOnboardingTourSeen_RepoError_PropagatesWrapped(t *testing.T) {
	repoErr := errors.New("db down")
	stub := &onboardingUserRepoStub{returnErr: repoErr}
	svc := NewUserService(stub, nil, nil, nil)

	err := svc.MarkOnboardingTourSeen(context.Background(), 7)
	require.Error(t, err)
	require.ErrorIs(t, err, repoErr, "repo error must be wrapped (errors.Is reachable)")
}

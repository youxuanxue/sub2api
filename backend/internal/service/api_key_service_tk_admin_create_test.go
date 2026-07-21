//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type userRepoStubForAdminCreate struct {
	userRepoStubForGroupUpdate
	user   *User
	getErr error
}

func (s *userRepoStubForAdminCreate) GetByID(context.Context, int64) (*User, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.user == nil {
		return nil, ErrUserNotFound
	}
	clone := *s.user
	return &clone, nil
}

type apiKeyRepoStubForAdminCreate struct {
	apiKeyRepoStubForGroupUpdate
	created *APIKey
}

func (s *apiKeyRepoStubForAdminCreate) Create(_ context.Context, key *APIKey) error {
	s.created = key
	key.ID = 99
	return nil
}

func (s *apiKeyRepoStubForAdminCreate) ExistsByKey(context.Context, string) (bool, error) {
	return false, nil
}

func newAdminCreateAPIKeyService(t *testing.T, user *User, group *Group) (*APIKeyService, *apiKeyRepoStubForAdminCreate) {
	t.Helper()
	repo := &apiKeyRepoStubForAdminCreate{}
	svc := &APIKeyService{
		apiKeyRepo: repo,
		userRepo:   &userRepoStubForAdminCreate{user: user},
		groupRepo:  &groupRepoStubForGroupUpdate{group: group},
		cfg:        &config.Config{Default: config.DefaultConfig{APIKeyPrefix: "tk_"}},
	}
	return svc, repo
}

func TestAPIKeyService_CreateAsAdmin_BypassesUserGroupPermission(t *testing.T) {
	groupID := int64(10)
	user := &User{ID: 1, Status: StatusActive, AllowedGroups: []int64{}}
	group := &Group{ID: groupID, Status: StatusActive, IsExclusive: true}

	svc, repo := newAdminCreateAPIKeyService(t, user, group)
	direct := RoutingModeDirect
	key, err := svc.CreateAsAdmin(context.Background(), user.ID, CreateAPIKeyRequest{
		Name:        "relay-us6",
		GroupID:     &groupID,
		RoutingMode: &direct,
	})
	require.NoError(t, err)
	require.NotNil(t, key)
	require.Equal(t, "relay-us6", key.Name)
	require.NotNil(t, repo.created)
	require.Equal(t, groupID, *repo.created.GroupID)
	require.True(t, len(key.Key) > len("tk_"))
	require.Equal(t, "tk_", key.Key[:3])
}

func TestAPIKeyService_CreateAsAdmin_UserNotFound(t *testing.T) {
	svc := &APIKeyService{
		userRepo: &userRepoStubForAdminCreate{getErr: ErrUserNotFound},
		cfg:      &config.Config{Default: config.DefaultConfig{APIKeyPrefix: "tk_"}},
	}
	_, err := svc.CreateAsAdmin(context.Background(), 404, CreateAPIKeyRequest{Name: "x"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUserNotFound)
}

func TestAPIKeyService_CreateAsAdmin_GroupNotActive(t *testing.T) {
	groupID := int64(10)
	user := &User{ID: 1, Status: StatusActive}
	group := &Group{ID: groupID, Status: StatusDisabled}

	svc, _ := newAdminCreateAPIKeyService(t, user, group)
	direct := RoutingModeDirect
	_, err := svc.CreateAsAdmin(context.Background(), user.ID, CreateAPIKeyRequest{
		Name:        "relay-us6",
		GroupID:     &groupID,
		RoutingMode: &direct,
	})
	require.Error(t, err)
	var appErr *infraerrors.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, "GROUP_NOT_ACTIVE", appErr.Reason)
}

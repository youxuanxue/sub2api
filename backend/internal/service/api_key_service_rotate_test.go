//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type rotateAPIKeyRepoStub struct {
	quotaBaseAPIKeyRepoStub
	key       *APIKey
	updateErr error
	updated   *APIKey
	fields    APIKeyUpdateFields
}

func (s *rotateAPIKeyRepoStub) GetByID(context.Context, int64) (*APIKey, error) {
	if s.key == nil {
		return nil, ErrAPIKeyNotFound
	}
	clone := *s.key
	return &clone, nil
}

func (s *rotateAPIKeyRepoStub) Update(_ context.Context, key *APIKey, fields APIKeyUpdateFields) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.fields = fields
	clone := *key
	s.updated = &clone
	s.key.Key = key.Key
	return nil
}

func TestAPIKeyRotatePreservesRecordAndReplacesCredential(t *testing.T) {
	repo := &rotateAPIKeyRepoStub{key: &APIKey{
		ID: 11, UserID: 7, Key: "sk-old", Name: "production", Status: StatusActive,
		Quota: 100, QuotaUsed: 12, RoutingMode: RoutingModeUniversal,
	}}
	cache := &quotaStateCacheStub{}
	svc := &APIKeyService{apiKeyRepo: repo, cache: cache}

	rotated, oldKey, err := svc.Rotate(context.Background(), 11, 7)

	require.NoError(t, err)
	require.Equal(t, "sk-old", oldKey)
	require.NotEqual(t, oldKey, rotated.Key)
	require.Equal(t, int64(11), rotated.ID)
	require.Equal(t, "production", rotated.Name)
	require.Equal(t, StatusActive, rotated.Status)
	require.Equal(t, RoutingModeUniversal, rotated.RoutingMode)
	require.Equal(t, APIKeyUpdateFields{Key: true}, repo.fields)
	require.Equal(t, rotated.Key, repo.key.Key)
	require.Equal(t, []string{svc.authCacheKey("sk-old"), svc.authCacheKey(rotated.Key)}, cache.deleteAuthKeys)
}

func TestAPIKeyRotateRejectsOtherOwnerWithoutWriting(t *testing.T) {
	repo := &rotateAPIKeyRepoStub{key: &APIKey{ID: 11, UserID: 7, Key: "sk-old"}}
	svc := &APIKeyService{apiKeyRepo: repo}

	_, _, err := svc.Rotate(context.Background(), 11, 99)

	require.ErrorIs(t, err, ErrInsufficientPerms)
	require.Nil(t, repo.updated)
	require.Empty(t, repo.fields)
	require.Equal(t, "sk-old", repo.key.Key)
}

func TestAPIKeyRotateKeepsOldCredentialWhenWriteFails(t *testing.T) {
	writeErr := errors.New("write failed")
	repo := &rotateAPIKeyRepoStub{key: &APIKey{ID: 11, UserID: 7, Key: "sk-old"}, updateErr: writeErr}
	svc := &APIKeyService{apiKeyRepo: repo}

	_, _, err := svc.Rotate(context.Background(), 11, 7)

	require.ErrorIs(t, err, writeErr)
	require.Equal(t, "sk-old", repo.key.Key)
}

//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type kiroSessionRecoveryStoreStub struct {
	GatewayCache
	groupID     int64
	sessionHash string
	accountID   int64
	ttl         time.Duration
	consumeCall int
}

func (s *kiroSessionRecoveryStoreStub) SetKiroSessionRecoveryExclusion(_ context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	s.groupID = groupID
	s.sessionHash = sessionHash
	s.accountID = accountID
	s.ttl = ttl
	return nil
}

func (s *kiroSessionRecoveryStoreStub) ConsumeKiroSessionRecoveryExclusion(_ context.Context, groupID int64, sessionHash string) (int64, error) {
	s.consumeCall++
	if groupID != s.groupID || sessionHash != s.sessionHash {
		return 0, nil
	}
	accountID := s.accountID
	s.accountID = 0
	return accountID, nil
}

func TestGatewayService_KiroSessionRecoveryIsOneShotAndSessionScoped(t *testing.T) {
	store := &kiroSessionRecoveryStoreStub{}
	svc := &GatewayService{cache: store}
	groupID := int64(7)

	require.NoError(t, svc.RememberKiroSessionRecovery(context.Background(), &groupID, "session-a", 99))
	require.Equal(t, groupID, store.groupID)
	require.Equal(t, "session-a", store.sessionHash)
	require.Equal(t, int64(99), store.accountID)
	require.Equal(t, stickySessionTTL, store.ttl, "TTL is stale-state garbage collection, not an account cooldown")

	otherSession, err := svc.ConsumeKiroSessionRecovery(context.Background(), &groupID, "session-b")
	require.NoError(t, err)
	require.Zero(t, otherSession, "another session must not inherit the exclusion")

	first, err := svc.ConsumeKiroSessionRecovery(context.Background(), &groupID, "session-a")
	require.NoError(t, err)
	require.Equal(t, int64(99), first)
	second, err := svc.ConsumeKiroSessionRecovery(context.Background(), &groupID, "session-a")
	require.NoError(t, err)
	require.Zero(t, second, "the failed account is excluded for exactly one continuation")
}

func TestGatewayService_KiroSessionRecoveryNoopsWithoutSessionIdentity(t *testing.T) {
	store := &kiroSessionRecoveryStoreStub{}
	svc := &GatewayService{cache: store}

	require.NoError(t, svc.RememberKiroSessionRecovery(context.Background(), nil, "", 99))
	require.Zero(t, store.accountID)
	accountID, err := svc.ConsumeKiroSessionRecovery(context.Background(), nil, "")
	require.NoError(t, err)
	require.Zero(t, accountID)
	require.Zero(t, store.consumeCall)
}

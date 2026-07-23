package service

import (
	"context"
	"time"
)

// KiroSessionRecoveryStore persists a one-shot account exclusion for the next
// request in a sticky session. It is optional so non-production cache stubs do
// not need to implement Kiro-specific behavior.
type KiroSessionRecoveryStore interface {
	SetKiroSessionRecoveryExclusion(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error
	ConsumeKiroSessionRecoveryExclusion(ctx context.Context, groupID int64, sessionHash string) (int64, error)
}

// RememberKiroSessionRecovery makes the next request in this session avoid the
// account that returned an incomplete post-output stream. The TTL only bounds
// stale Redis state; it is not an account cooldown.
func (s *GatewayService) RememberKiroSessionRecovery(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
	if s == nil || s.cache == nil || sessionHash == "" || accountID <= 0 {
		return nil
	}
	store, ok := s.cache.(KiroSessionRecoveryStore)
	if !ok {
		return nil
	}
	return store.SetKiroSessionRecoveryExclusion(ctx, derefGroupID(groupID), sessionHash, accountID, stickySessionTTL)
}

// ConsumeKiroSessionRecovery returns and atomically removes the one-shot
// exclusion, so concurrent continuations cannot repeatedly penalize an account.
func (s *GatewayService) ConsumeKiroSessionRecovery(ctx context.Context, groupID *int64, sessionHash string) (int64, error) {
	if s == nil || s.cache == nil || sessionHash == "" {
		return 0, nil
	}
	store, ok := s.cache.(KiroSessionRecoveryStore)
	if !ok {
		return 0, nil
	}
	return store.ConsumeKiroSessionRecoveryExclusion(ctx, derefGroupID(groupID), sessionHash)
}

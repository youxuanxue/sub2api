package service

import (
	"context"
	"time"
)

// The WS store also holds process-local turn/connection state. It must use the
// same identity as Redis soft affinity without changing the billing group.
type candidateWSStateStore struct {
	OpenAIWSStateStore
	ctx      context.Context
	identity candidateIdentity
}

func (s *OpenAIGatewayService) getCandidateWSStateStore(ctx context.Context) OpenAIWSStateStore {
	store := s.getOpenAIWSStateStore()
	if identity, ok := candidateIdentityFromContext(ctx); ok {
		return candidateWSStateStore{OpenAIWSStateStore: store, ctx: ctx, identity: identity}
	}
	return store
}

func (s candidateWSStateStore) BindSessionTurnState(groupID int64, sessionHash, turnState string, ttl time.Duration) {
	groupID, sessionHash = CandidateAffinityCacheScope(s.ctx, groupID, sessionHash)
	s.OpenAIWSStateStore.BindSessionTurnState(groupID, sessionHash, turnState, ttl)
}

func (s candidateWSStateStore) GetSessionTurnState(groupID int64, sessionHash string) (string, bool) {
	groupID, sessionHash = CandidateAffinityCacheScope(s.ctx, groupID, sessionHash)
	return s.OpenAIWSStateStore.GetSessionTurnState(groupID, sessionHash)
}

func (s candidateWSStateStore) DeleteSessionTurnState(groupID int64, sessionHash string) {
	groupID, sessionHash = CandidateAffinityCacheScope(s.ctx, groupID, sessionHash)
	s.OpenAIWSStateStore.DeleteSessionTurnState(groupID, sessionHash)
}

func (s candidateWSStateStore) BindSessionConn(groupID int64, sessionHash, connID string, ttl time.Duration) {
	groupID, sessionHash = CandidateAffinityCacheScope(s.ctx, groupID, sessionHash)
	s.OpenAIWSStateStore.BindSessionConn(groupID, sessionHash, connID, ttl)
}

func (s candidateWSStateStore) GetSessionConn(groupID int64, sessionHash string) (string, bool) {
	groupID, sessionHash = CandidateAffinityCacheScope(s.ctx, groupID, sessionHash)
	return s.OpenAIWSStateStore.GetSessionConn(groupID, sessionHash)
}

func (s candidateWSStateStore) DeleteSessionConn(groupID int64, sessionHash string) {
	groupID, sessionHash = CandidateAffinityCacheScope(s.ctx, groupID, sessionHash)
	s.OpenAIWSStateStore.DeleteSessionConn(groupID, sessionHash)
}

func (s candidateWSStateStore) BindResponseConn(responseID, connID string, ttl time.Duration) {
	s.OpenAIWSStateStore.BindResponseConn(candidateResponseID(s.identity.userID, responseID), connID, ttl)
}

func (s candidateWSStateStore) GetResponseConn(responseID string) (string, bool) {
	return s.OpenAIWSStateStore.GetResponseConn(candidateResponseID(s.identity.userID, responseID))
}

func (s candidateWSStateStore) DeleteResponseConn(responseID string) {
	s.OpenAIWSStateStore.DeleteResponseConn(candidateResponseID(s.identity.userID, responseID))
}

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type candidateIdentityContextKey struct{}

type candidateIdentity struct {
	userID int64
	keyID  int64
}

// WithCandidateIdentity installs authenticated identity before candidate lookup.
// Billing-origin rebinding must retain this context.
func WithCandidateIdentity(ctx context.Context, userID, keyID int64) context.Context {
	return context.WithValue(ctx, candidateIdentityContextKey{}, candidateIdentity{userID: userID, keyID: keyID})
}

func candidateIdentityFromContext(ctx context.Context) (candidateIdentity, bool) {
	if ctx == nil {
		return candidateIdentity{}, false
	}
	identity, ok := ctx.Value(candidateIdentityContextKey{}).(candidateIdentity)
	return identity, ok && identity.userID > 0 && identity.keyID > 0
}

// CandidateAffinityCacheScope scopes soft affinity to a tenant and key, while
// preserving the explicit namespaces used for hard Responses continuation.
func CandidateAffinityCacheScope(ctx context.Context, groupID int64, sessionHash string) (int64, string) {
	if strings.TrimSpace(sessionHash) == "" ||
		strings.HasPrefix(sessionHash, openAIWSResponseAccountCachePrefix) ||
		strings.HasPrefix(sessionHash, openAIHTTPResponseOwnerUserPrefix) ||
		strings.HasPrefix(sessionHash, openAIHTTPResponseOwnerKeyPrefix) ||
		strings.HasPrefix(sessionHash, "openai:grok-video:") {
		return groupID, sessionHash
	}
	if identity, ok := candidateIdentityFromContext(ctx); ok {
		return 0, fmt.Sprintf("candidate:v1:user:%d:key:%d:%s", identity.userID, identity.keyID, sessionHash)
	}
	return groupID, sessionHash
}

func candidateResponseID(userID int64, responseID string) string {
	responseID = normalizeOpenAIWSResponseID(responseID)
	if userID <= 0 || responseID == "" {
		return ""
	}
	return fmt.Sprintf("candidate:v1:user:%d:%s", userID, responseID)
}

var ErrCandidateContinuationUnavailable = errors.New("previous_response_id is unavailable or not authorized")

// ResolveCandidateContinuation resolves the original account before payment
// tier filtering. The caller must still require an eligible authorized path to
// that account. Legacy reads are bounded by already-authorized groups.
func (s *OpenAIGatewayService) ResolveCandidateContinuation(ctx context.Context, apiKey *APIKey, authorizedGroups []Group, previousResponseID string) (int64, error) {
	if strings.TrimSpace(previousResponseID) == "" {
		return 0, nil
	}
	if s == nil || apiKey == nil || apiKey.UserID <= 0 || apiKey.ID <= 0 || len(authorizedGroups) == 0 {
		return 0, ErrCandidateContinuationUnavailable
	}
	// Resolve each owner/account pair from one namespace. Mixing a new owner
	// record with an old account record would cross tenant boundaries on an ID
	// collision or a partially written binding.
	ctx = WithCandidateIdentity(ctx, 0, 0)
	store := s.getOpenAIWSStateStore()
	lookup := func(groupID int64, responseID string) (int64, error) {
		ownerUserID, _, found, err := store.GetHTTPResponseOwner(ctx, groupID, responseID)
		if err != nil {
			return 0, fmt.Errorf("resolve continuation owner: %w", err)
		}
		if !found || ownerUserID != apiKey.UserID {
			return 0, nil
		}
		accountID, err := store.GetResponseAccount(ctx, groupID, responseID)
		if err != nil {
			return 0, fmt.Errorf("resolve continuation account: %w", err)
		}
		return accountID, nil
	}
	if accountID, err := lookup(0, candidateResponseID(apiKey.UserID, previousResponseID)); err != nil || accountID > 0 {
		return accountID, err
	}
	seen := make(map[int64]struct{}, len(authorizedGroups))
	for _, group := range authorizedGroups {
		if group.ID <= 0 {
			continue
		}
		if _, exists := seen[group.ID]; exists {
			continue
		}
		seen[group.ID] = struct{}{}
		if accountID, err := lookup(group.ID, previousResponseID); err != nil || accountID > 0 {
			return accountID, err
		}
	}
	return 0, ErrCandidateContinuationUnavailable
}

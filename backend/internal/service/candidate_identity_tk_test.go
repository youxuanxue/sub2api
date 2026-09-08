package service

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type candidateIdentityTestCache struct {
	GatewayCache
	entries  map[string]int64
	reads    []int64
	readErr  error
	writeErr error
}

func (c *candidateIdentityTestCache) GetSessionAccountID(_ context.Context, groupID int64, sessionHash string) (int64, error) {
	c.reads = append(c.reads, groupID)
	if c.readErr != nil {
		return 0, c.readErr
	}
	if id, ok := c.entries[fmt.Sprintf("%d:%s", groupID, sessionHash)]; ok {
		return id, nil
	}
	return 0, ErrStickySessionNotFound
}

func (c *candidateIdentityTestCache) SetSessionAccountID(_ context.Context, groupID int64, sessionHash string, accountID int64, _ time.Duration) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	if c.entries == nil {
		c.entries = make(map[string]int64)
	}
	c.entries[fmt.Sprintf("%d:%s", groupID, sessionHash)] = accountID
	return nil
}

func (c *candidateIdentityTestCache) DeleteSessionAccountID(_ context.Context, groupID int64, sessionHash string) error {
	delete(c.entries, fmt.Sprintf("%d:%s", groupID, sessionHash))
	return nil
}

type candidateIdentityFirstWriteFailureCache struct {
	*candidateIdentityTestCache
	failed bool
}

func (c *candidateIdentityFirstWriteFailureCache) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	if !c.failed {
		c.failed = true
		return errors.New("first binding write failed")
	}
	return c.candidateIdentityTestCache.SetSessionAccountID(ctx, groupID, sessionHash, accountID, ttl)
}

func TestCandidateContinuation_NewBindingSurvivesBillingOriginAndKeyChange(t *testing.T) {
	cache := &candidateIdentityTestCache{}
	writer := NewOpenAIWSStateStore(cache)
	ctx := WithCandidateIdentity(context.Background(), 10, 20)
	require.NoError(t, writer.BindResponseAccount(ctx, 1, "resp_owned", 115, time.Hour))
	reader := &OpenAIGatewayService{cache: cache}
	accountID, err := reader.ResolveCandidateContinuation(context.Background(), &APIKey{UserID: 10, ID: 21}, []Group{{ID: 11}}, "resp_owned")
	require.NoError(t, err)
	require.Equal(t, int64(115), accountID)

	// A previous binary can still read the original group namespace.
	rollback := NewOpenAIWSStateStore(cache)
	accountID, err = rollback.GetResponseAccount(context.Background(), 1, "resp_owned")
	require.NoError(t, err)
	require.Equal(t, int64(115), accountID)
	userID, keyID, found, err := rollback.GetHTTPResponseOwner(context.Background(), 1, "resp_owned")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(10), userID)
	require.Equal(t, int64(20), keyID)

	_, err = reader.ResolveCandidateContinuation(context.Background(), &APIKey{UserID: 99, ID: 20}, []Group{{ID: 1}, {ID: 11}}, "resp_owned")
	require.ErrorIs(t, err, ErrCandidateContinuationUnavailable)
}

func TestCandidateContinuation_LegacyReadsOnlyAuthorizedOrigins(t *testing.T) {
	cache := &candidateIdentityTestCache{}
	legacy, ok := NewOpenAIWSStateStore(cache).(*defaultOpenAIWSStateStore)
	require.True(t, ok)
	ctx := context.Background()
	require.NoError(t, legacy.bindResponseAccount(ctx, 1, "resp_legacy", 115, time.Hour))
	require.NoError(t, legacy.bindHTTPResponseOwner(ctx, 1, "resp_legacy", 10, 20, time.Hour))
	reader := &OpenAIGatewayService{cache: cache}
	key := &APIKey{UserID: 10, ID: 21}
	_, err := reader.ResolveCandidateContinuation(ctx, key, []Group{{ID: 11}}, "resp_legacy")
	require.ErrorIs(t, err, ErrCandidateContinuationUnavailable)
	for _, groupID := range cache.reads {
		require.Contains(t, []int64{0, 11}, groupID)
	}
	accountID, err := reader.ResolveCandidateContinuation(ctx, key, []Group{{ID: 11}, {ID: 1}}, "resp_legacy")
	require.NoError(t, err)
	require.Equal(t, int64(115), accountID)
}

func TestCandidateContinuation_OwnerRefreshCannotReplaceTenantAccount(t *testing.T) {
	cache := &candidateIdentityTestCache{}
	writer := NewOpenAIWSStateStore(cache)
	first := WithCandidateIdentity(context.Background(), 10, 20)
	second := WithCandidateIdentity(context.Background(), 99, 98)
	require.NoError(t, writer.BindResponseAccount(first, 1, "resp_collision", 115, time.Hour))
	require.NoError(t, writer.BindResponseAccount(second, 1, "resp_collision", 124, time.Hour))
	// HTTP forwarding also refreshes the owner after binding the account. Another
	// upstream may have replaced the legacy group record during that interval.
	require.NoError(t, writer.BindHTTPResponseOwner(first, 1, "resp_collision", 10, 20, time.Hour))
	reader := &OpenAIGatewayService{cache: cache}
	for _, test := range []struct {
		userID    int64
		keyID     int64
		accountID int64
	}{{10, 20, 115}, {99, 98, 124}} {
		accountID, err := reader.ResolveCandidateContinuation(context.Background(), &APIKey{UserID: test.userID, ID: test.keyID}, []Group{{ID: 1}}, "resp_collision")
		require.NoError(t, err)
		require.Equal(t, test.accountID, accountID)
	}
}

func TestCandidateContinuation_UnknownUnownedAndFailedLookupsDoNotBecomeNewRequests(t *testing.T) {
	cache := &candidateIdentityTestCache{}
	ctx := context.Background()
	store := NewOpenAIWSStateStore(cache)
	require.NoError(t, store.BindResponseAccount(ctx, 1, "resp_unowned", 115, time.Hour))
	svc := &OpenAIGatewayService{cache: cache}
	key := &APIKey{UserID: 10, ID: 20}
	for _, responseID := range []string{"resp_unknown", "resp_unowned"} {
		accountID, err := svc.ResolveCandidateContinuation(ctx, key, []Group{{ID: 1}}, responseID)
		require.Zero(t, accountID)
		require.ErrorIs(t, err, ErrCandidateContinuationUnavailable)
	}
	cache.readErr = errors.New("redis unavailable")
	_, err := svc.ResolveCandidateContinuation(ctx, key, []Group{{ID: 1}}, "resp_unknown")
	require.ErrorIs(t, err, cache.readErr)
}

func TestCandidateContinuation_DoesNotMixOwnerAndAccountNamespaces(t *testing.T) {
	cache := &candidateIdentityTestCache{}
	store, ok := NewOpenAIWSStateStore(cache).(*defaultOpenAIWSStateStore)
	require.True(t, ok)
	ctx := context.Background()
	require.NoError(t, store.bindHTTPResponseOwner(ctx, 0, candidateResponseID(10, "resp_collision"), 10, 20, time.Hour))
	require.NoError(t, store.bindResponseAccount(ctx, 1, "resp_collision", 115, time.Hour))
	require.NoError(t, store.bindHTTPResponseOwner(ctx, 1, "resp_collision", 99, 98, time.Hour))
	svc := &OpenAIGatewayService{cache: cache}
	_, err := svc.ResolveCandidateContinuation(ctx, &APIKey{UserID: 10, ID: 20}, []Group{{ID: 1}}, "resp_collision")
	require.ErrorIs(t, err, ErrCandidateContinuationUnavailable)
}

func TestCandidateContinuation_FailedBindingDoesNotCreateLocalSuccess(t *testing.T) {
	cache := &candidateIdentityTestCache{writeErr: errors.New("redis unavailable")}
	store := NewOpenAIWSStateStore(cache)
	ctx := WithCandidateIdentity(context.Background(), 10, 20)
	require.ErrorIs(t, store.BindResponseAccount(ctx, 1, "resp_failed", 115, time.Hour), cache.writeErr)
	accountID, err := store.GetResponseAccount(ctx, 1, "resp_failed")
	require.NoError(t, err)
	require.Zero(t, accountID)
}

func TestCandidateContinuation_FailedHTTPAccountBindingDoesNotPublishOwner(t *testing.T) {
	cache := &candidateIdentityFirstWriteFailureCache{candidateIdentityTestCache: &candidateIdentityTestCache{}}
	svc := &OpenAIGatewayService{cache: cache}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetOpenAIHTTPResponseOwner(c, 10, 20)
	svc.bindHTTPResponseAccount(WithCandidateIdentity(context.Background(), 10, 20), c, &Account{ID: 115}, "resp_failed")
	require.True(t, cache.failed)
	require.Empty(t, cache.entries)
}

func TestCandidateWSState_SessionScopeAndResponseOwner(t *testing.T) {
	svc := &OpenAIGatewayService{}
	owner := svc.getCandidateWSStateStore(WithCandidateIdentity(context.Background(), 10, 20))
	owner.BindSessionTurnState(1, "session", "turn", time.Hour)
	owner.BindSessionConn(1, "session", "connection", time.Hour)
	owner.BindResponseConn("resp_owned", "connection", time.Hour)
	turn, found := owner.GetSessionTurnState(11, "session")
	require.True(t, found)
	require.Equal(t, "turn", turn)
	conn, found := owner.GetSessionConn(11, "session")
	require.True(t, found)
	require.Equal(t, "connection", conn)
	newKey := svc.getCandidateWSStateStore(WithCandidateIdentity(context.Background(), 10, 21))
	_, found = newKey.GetSessionConn(11, "session")
	require.False(t, found)
	conn, found = newKey.GetResponseConn("resp_owned")
	require.True(t, found)
	require.Equal(t, "connection", conn)
	otherUser := svc.getCandidateWSStateStore(WithCandidateIdentity(context.Background(), 99, 20))
	_, found = otherUser.GetSessionTurnState(1, "session")
	require.False(t, found)
	_, found = otherUser.GetResponseConn("resp_owned")
	require.False(t, found)
	owner.DeleteSessionConn(11, "session")
	_, found = owner.GetSessionConn(1, "session")
	require.False(t, found)
}

func TestCandidateWSState_PreemptionIgnoresBillingOriginAndPreservesTenantIsolation(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 115, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	identity := WithCandidateIdentity(context.Background(), 10, 20)
	first, cleanupFirst, armed, _ := svc.beginOpenAIWSSessionPreemptContext(identity, account, 1, 20, "session", false)
	t.Cleanup(cleanupFirst)
	require.True(t, armed)
	state := svc.getCandidateWSStateStore(identity)
	state.BindSessionConn(1, "session", "old-connection", time.Hour)
	second, cleanupSecond, armed, preempted := svc.beginOpenAIWSSessionPreemptContext(identity, account, 11, 20, "session", false)
	t.Cleanup(cleanupSecond)
	require.True(t, armed)
	require.True(t, preempted)
	require.ErrorIs(t, context.Cause(first), errOpenAIWSSessionPreempted)
	require.NoError(t, context.Cause(second))
	_, found := state.GetSessionConn(11, "session")
	require.False(t, found)
	other := WithCandidateIdentity(context.Background(), 99, 20)
	_, cleanupOther, armed, preempted := svc.beginOpenAIWSSessionPreemptContext(other, account, 11, 20, "session", false)
	t.Cleanup(cleanupOther)
	require.True(t, armed)
	require.False(t, preempted)
	require.NoError(t, context.Cause(second))
}

func TestCandidateDigestSession_IgnoresBillingOriginAndPreservesTenantIsolation(t *testing.T) {
	svc := &GatewayService{digestStore: NewDigestSessionStore()}
	ctx := WithCandidateIdentity(context.Background(), 10, 20)
	require.NoError(t, svc.SaveGeminiSession(ctx, 1, "gemini-prefix", "a-b", "session-uuid", 115, ""))
	uuid, accountID, _, found := svc.FindGeminiSession(ctx, 11, "gemini-prefix", "a-b-c")
	require.True(t, found)
	require.Equal(t, "session-uuid", uuid)
	require.Equal(t, int64(115), accountID)
	_, _, _, found = svc.FindGeminiSession(WithCandidateIdentity(ctx, 99, 20), 1, "gemini-prefix", "a-b-c")
	require.False(t, found)
	require.NoError(t, svc.SaveAnthropicSession(ctx, 1, "anthropic-prefix", "a-b", "anthropic-session", 116, ""))
	uuid, accountID, _, found = svc.FindAnthropicSession(ctx, 11, "anthropic-prefix", "a-b-c")
	require.True(t, found)
	require.Equal(t, "anthropic-session", uuid)
	require.Equal(t, int64(116), accountID)
}

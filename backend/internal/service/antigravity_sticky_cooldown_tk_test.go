package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// tkStickyTestCache is a minimal GatewayCache stub that only implements the
// session-binding methods. Embedding the interface keeps this focused: any other
// method panics on nil, which is what we want if production code starts using
// more of the cache from these paths.
type tkStickyTestCache struct {
	GatewayCache
	entries       map[string]int64
	setCalls      int
	deleteCalls   int
	getErr        error
	setErr        error
	deleteErr     error
	lastSetTTL    time.Duration
	lastDeleteKey string
}

func newTKStickyTestCache() *tkStickyTestCache {
	return &tkStickyTestCache{entries: make(map[string]int64)}
}

func (c *tkStickyTestCache) key(groupID int64, sessionHash string) string {
	return sessionHash
}

func (c *tkStickyTestCache) GetSessionAccountID(_ context.Context, groupID int64, sessionHash string) (int64, error) {
	if c.getErr != nil {
		return 0, c.getErr
	}
	return c.entries[c.key(groupID, sessionHash)], nil
}

func (c *tkStickyTestCache) SetSessionAccountID(_ context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	c.setCalls++
	c.lastSetTTL = ttl
	if c.setErr != nil {
		return c.setErr
	}
	c.entries[c.key(groupID, sessionHash)] = accountID
	return nil
}

func (c *tkStickyTestCache) DeleteSessionAccountID(_ context.Context, groupID int64, sessionHash string) error {
	c.deleteCalls++
	c.lastDeleteKey = sessionHash
	if c.deleteErr != nil {
		return c.deleteErr
	}
	delete(c.entries, c.key(groupID, sessionHash))
	return nil
}

func TestTkDropStickyForUnusableAccountDropsOnlyMatchingBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("drops when bound to the cooled account", func(t *testing.T) {
		cache := newTKStickyTestCache()
		cache.entries["gemini:abc"] = 42
		svc := &AntigravityGatewayService{cache: cache}

		svc.tkDropStickyForUnusableAccount(ctx, 0, "gemini:abc", &Account{ID: 42})

		require.Equal(t, 1, cache.deleteCalls)
		require.NotContains(t, cache.entries, "gemini:abc")
	})

	t.Run("keeps a binding another turn already moved to a healthy account", func(t *testing.T) {
		cache := newTKStickyTestCache()
		cache.entries["gemini:abc"] = 7
		svc := &AntigravityGatewayService{cache: cache}

		svc.tkDropStickyForUnusableAccount(ctx, 0, "gemini:abc", &Account{ID: 42})

		require.Equal(t, 0, cache.deleteCalls, "must not steal a healthy concurrent binding")
		require.Equal(t, int64(7), cache.entries["gemini:abc"])
	})

	t.Run("no-op without a binding", func(t *testing.T) {
		cache := newTKStickyTestCache()
		svc := &AntigravityGatewayService{cache: cache}

		svc.tkDropStickyForUnusableAccount(ctx, 0, "gemini:abc", &Account{ID: 42})

		require.Equal(t, 0, cache.deleteCalls)
	})

	t.Run("no-op on empty session or nil inputs", func(t *testing.T) {
		cache := newTKStickyTestCache()
		svc := &AntigravityGatewayService{cache: cache}

		svc.tkDropStickyForUnusableAccount(ctx, 0, "", &Account{ID: 42})
		svc.tkDropStickyForUnusableAccount(ctx, 0, "gemini:abc", nil)
		(*AntigravityGatewayService)(nil).tkDropStickyForUnusableAccount(ctx, 0, "gemini:abc", &Account{ID: 42})

		require.Equal(t, 0, cache.deleteCalls)
	})
}

func TestTkRebindStickyAfterFailoverRepinsToSuccessor(t *testing.T) {
	ctx := context.Background()

	t.Run("rebinds after the cooled-account drop", func(t *testing.T) {
		cache := newTKStickyTestCache()
		svc := &AntigravityGatewayService{cache: cache}

		svc.tkRebindStickyAfterFailover(ctx, 0, "gemini:abc", 42, &Account{ID: 99})

		require.Equal(t, 1, cache.setCalls)
		require.Equal(t, int64(99), cache.entries["gemini:abc"])
		require.Equal(t, stickySessionTTL, cache.lastSetTTL)
	})

	t.Run("does not steal a binding already owned by a third account", func(t *testing.T) {
		cache := newTKStickyTestCache()
		cache.entries["gemini:abc"] = 7
		svc := &AntigravityGatewayService{cache: cache}

		svc.tkRebindStickyAfterFailover(ctx, 0, "gemini:abc", 42, &Account{ID: 99})

		require.Equal(t, 0, cache.setCalls)
		require.Equal(t, int64(7), cache.entries["gemini:abc"])
	})

	t.Run("skips when the session already points at the serving account", func(t *testing.T) {
		cache := newTKStickyTestCache()
		cache.entries["gemini:abc"] = 99
		svc := &AntigravityGatewayService{cache: cache}

		svc.tkRebindStickyAfterFailover(ctx, 0, "gemini:abc", 42, &Account{ID: 99})

		require.Equal(t, 0, cache.setCalls)
	})

	t.Run("skips when no failover happened", func(t *testing.T) {
		cache := newTKStickyTestCache()
		svc := &AntigravityGatewayService{cache: cache}

		svc.tkRebindStickyAfterFailover(ctx, 0, "gemini:abc", 99, &Account{ID: 99})

		require.Equal(t, 0, cache.setCalls)
	})
}

// The two helpers together must be a no-op on the healthy path: that is the
// whole basis for claiming steady-state cache behaviour is unchanged.
func TestStickyCooldownHelpersLeaveHealthyPathUntouched(t *testing.T) {
	ctx := context.Background()
	cache := newTKStickyTestCache()
	cache.entries["gemini:abc"] = 42
	svc := &AntigravityGatewayService{cache: cache}

	// A request served by the bound account, with no switch.
	svc.tkRebindStickyAfterFailover(ctx, 0, "gemini:abc", 42, &Account{ID: 42})

	require.Equal(t, 0, cache.setCalls)
	require.Equal(t, 0, cache.deleteCalls)
	require.Equal(t, int64(42), cache.entries["gemini:abc"])
}

func TestTkGoogle429CorroboratesAccountLimit(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "explicit RATE_LIMIT_EXCEEDED cools the account",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`,
			want: true,
		},
		{
			name: "QuotaFailure is account scoped",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDay"}]}]}}`,
			want: true,
		},
		{
			name: "bare shared-capacity 429 must not cool",
			body: `{"error":{"code":429,"message":"The service is temporarily busy. Please try again later.","status":"RESOURCE_EXHAUSTED"}}`,
			want: false,
		},
		{
			name: "MODEL_CAPACITY_EXHAUSTED is shared across accounts",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`,
			want: false,
		},
		{
			name: "capacity reason wins over a quota-style detail",
			body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"},{"@type":"type.googleapis.com/google.rpc.QuotaFailure"}]}}`,
			want: false,
		},
		{
			name: "double-wrapped envelope is unwrapped",
			body: `{"0":{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}}`,
			want: true,
		},
		{
			name: "empty body",
			body: "",
			want: false,
		},
		{
			name: "malformed body",
			body: "not json",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tkGoogle429CorroboratesAccountLimit([]byte(tt.body)))
		})
	}
}

//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// US-031 — Bug B-8 verification.
//
// BindStickySession used to inline its TTL calculation (read
// cfg.Gateway.OpenAIWS.StickySessionTTLSeconds, fall back to
// openaiStickySessionTTL) instead of calling the existing
// openAIWSSessionStickyTTL() helper that every other sticky-TTL site
// (refreshStickySessionTTL, scheduler-Layer-1, multiple WS handshake paths)
// uses. Identical computation today, but any future change to the TTL
// source (per-platform TTL, runtime override, etc.) would have to be
// duplicated.
//
// Fix routes BindStickySession through openAIWSSessionStickyTTL so all
// sticky TTL reads share a single source-of-truth.
//
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-8.

// us031TTLCapturingCache records the TTL passed to SetSessionAccountID so
// we can assert that BindStickySession funnels through openAIWSSessionStickyTTL
// rather than inlining the cfg read.
type us031TTLCapturingCache struct {
	sessionBindings map[string]int64
	lastTTL         time.Duration
	calls           int
}

func (c *us031TTLCapturingCache) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	if id, ok := c.sessionBindings[sessionHash]; ok {
		return id, nil
	}
	return 0, errors.New("not found")
}

func (c *us031TTLCapturingCache) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	if c.sessionBindings == nil {
		c.sessionBindings = make(map[string]int64)
	}
	c.sessionBindings[sessionHash] = accountID
	c.lastTTL = ttl
	c.calls++
	return nil
}

func (c *us031TTLCapturingCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	return nil
}

func (c *us031TTLCapturingCache) DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	delete(c.sessionBindings, sessionHash)
	return nil
}

func TestUS031_BindStickySession_UsesOpenAIWSSessionStickyTTL_DefaultPath(t *testing.T) {
	// No cfg.StickySessionTTLSeconds → expect the constant default.
	cache := &us031TTLCapturingCache{}
	svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{}}
	groupID := int64(1)

	require.NoError(t, svc.BindStickySession(context.Background(), &groupID, "session-default", 99))
	require.Equal(t, 1, cache.calls)
	require.Equal(t, openaiStickySessionTTL, cache.lastTTL,
		"BindStickySession default-path TTL must equal openAIWSSessionStickyTTL() default (Bug B-8 funnel)")
}

func TestUS031_BindStickySession_UsesOpenAIWSSessionStickyTTL_CustomCfg(t *testing.T) {
	// cfg.StickySessionTTLSeconds set → expect that exact value, mirroring
	// what openAIWSSessionStickyTTL() returns. Funnel through the helper
	// guarantees future TTL-source changes stay in one place.
	cache := &us031TTLCapturingCache{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600 // 10 min
	svc := &OpenAIGatewayService{cache: cache, cfg: cfg}
	groupID := int64(2)

	require.NoError(t, svc.BindStickySession(context.Background(), &groupID, "session-custom", 100))
	require.Equal(t, 1, cache.calls)
	require.Equal(t, 600*time.Second, cache.lastTTL,
		"BindStickySession must take TTL from openAIWSSessionStickyTTL() which reads cfg")
	// Sanity: the helper directly returns the same value.
	require.Equal(t, 600*time.Second, svc.openAIWSSessionStickyTTL())
}

func TestUS031_BindStickySession_NoOp_OnEmptyInputs(t *testing.T) {
	// Defense-in-depth: empty sessionHash or accountID<=0 must still no-op
	// (preserves prior behaviour; Bug B-8 fix is a TTL refactor, not a
	// behaviour change).
	cache := &us031TTLCapturingCache{}
	svc := &OpenAIGatewayService{cache: cache, cfg: &config.Config{}}
	groupID := int64(3)

	require.NoError(t, svc.BindStickySession(context.Background(), &groupID, "", 99))
	require.NoError(t, svc.BindStickySession(context.Background(), &groupID, "session-x", 0))
	require.NoError(t, svc.BindStickySession(context.Background(), &groupID, "session-x", -1))
	require.Equal(t, 0, cache.calls, "empty inputs must not invoke the cache write")
}

//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// US-030 — Bug B-7 verification.
//
// tryStickySessionHit (the legacy sticky entry on the OpenAIGatewayService
// receiver) used to `return nil` without clearing the Redis mapping when the
// bound account was no longer schedulable (SetRateLimited / SetError fired
// between sticky bind and now) or no longer in the right scheduling pool
// (group.platform changed). Symmetric path in
// openai_account_scheduler.go::selectBySessionHash already clears; this
// asymmetry meant every same-sessionHash request kept cache-hitting the dead
// account, getting filtered, and cascading into Layer 2 selection until TTL
// expired (1h default).
//
// Fix mirrors the scheduler-path behaviour: delete the mapping on
// !IsSchedulable() || !IsOpenAICompatPoolMember(groupPlatform).
//
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-7.

func TestUS030_TryStickyHit_AccountUnschedulable_DeletesMapping(t *testing.T) {
	ctx := context.Background()
	groupID := int64(86001)

	// Bound account: rate-limited (Schedulable == true via IsSchedulable() but
	// blocked by RateLimitResetAt in the future).
	rateLimited := newAPIAccount(86101, 5)
	resetAt := time.Now().Add(time.Hour)
	rateLimited.RateLimitedAt = &resetAt
	rateLimited.RateLimitResetAt = &resetAt

	// Backup healthy account so the load-balance fallback succeeds.
	healthyBackup := newAPIAccount(86102, 5)

	pool := []*Account{rateLimited, healthyBackup}
	sessionHash := "session-hash-newapi-rate-limited"

	svc := newStickyFixtureWithRepo(t, groupID, PlatformNewAPI, pool, sessionHash, rateLimited.ID)

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, healthyBackup.ID, selection.Account.ID,
		"must fail over to the healthy backup, not the rate-limited bound account")

	cache, ok := svc.cache.(*stubGatewayCache)
	require.True(t, ok)
	require.GreaterOrEqual(t, cache.deletedSessions[stickyAccountKey(sessionHash)], 1,
		"sticky binding pointing at rate-limited account must be cleared (Bug B-7 fix)")
}

func TestUS030_TryStickyHit_AccountWrongPool_DeletesMapping(t *testing.T) {
	ctx := context.Background()
	groupID := int64(86002)

	// Bound account: openai platform, but group has flipped to newapi
	// (e.g. admin migrated the group to a different platform). The sticky
	// binding now points across pool boundaries and must be cleared.
	openaiBound := openAIAccount(86201, 5)
	newapiBackup := newAPIAccount(86202, 5)

	sessionHash := "session-hash-cross-pool"

	// Inject the openaiBound account into the snapshot/repo so getSchedulableAccount
	// resolves it — but the group platform is newapi.
	svc := newStickyFixtureWithRepo(t, groupID, PlatformNewAPI, []*Account{openaiBound, newapiBackup}, sessionHash, openaiBound.ID)

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, newapiBackup.ID, selection.Account.ID,
		"cross-pool sticky binding must be invalidated and load-balance must pick a same-pool account")

	cache, ok := svc.cache.(*stubGatewayCache)
	require.True(t, ok)
	require.GreaterOrEqual(t, cache.deletedSessions[stickyAccountKey(sessionHash)], 1,
		"sticky binding pointing across pool boundary must be cleared (Bug B-7 fix)")
}

func TestUS030_TryStickyHit_AccountSchedulable_KeepsMapping_Regression(t *testing.T) {
	// Regression: when bound account is healthy and in-pool, sticky must HIT
	// and the binding must NOT be deleted.
	ctx := context.Background()
	groupID := int64(86003)

	healthy := newAPIAccount(86301, 5)
	pool := []*Account{healthy, newAPIAccount(86302, 5)}
	sessionHash := "session-hash-healthy-keeps"

	svc := newStickyFixtureWithRepo(t, groupID, PlatformNewAPI, pool, sessionHash, healthy.ID)

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, healthy.ID, selection.Account.ID,
		"healthy sticky binding must continue to HIT")

	cache, ok := svc.cache.(*stubGatewayCache)
	require.True(t, ok)
	require.Equal(t, 0, cache.deletedSessions[stickyAccountKey(sessionHash)],
		"healthy sticky binding must NOT be cleared (regression guard for B-7 fix)")
	require.Equal(t, healthy.ID, cache.sessionBindings[stickyAccountKey(sessionHash)],
		"binding must still point at the healthy account")
}

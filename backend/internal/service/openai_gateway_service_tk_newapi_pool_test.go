//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Tests for docs/approved/newapi-as-fifth-platform.md §3.1 U5 — sticky-session
// drift defense. Covers the AC subset of US-011 (cross-platform sticky leak)
// and US-013 (newapi sticky failover) that exercises the
// IsOpenAICompatPoolMember check inside selectBySessionHash and
// tryStickySessionHit, without standing up Redis or PostgreSQL.

// Note: stubOpenAIAccountRepo, stubGatewayCache, openAISnapshotCacheStub,
// stubConcurrencyCache, stubSchedulerGroupRepo, openAIAccount, newAPIAccount
// are defined in the unit-test fixtures of this package
// (openai_gateway_service_test.go, openai_account_scheduler_test.go,
// openai_account_scheduler_tk_newapi_test.go).

func newStickyFixture(t *testing.T, groupID int64, groupPlatform string, pool []*Account, sessionHash string, stickyAccountID int64) *OpenAIGatewayService {
	t.Helper()
	accountsByID := make(map[int64]*Account, len(pool))
	for _, p := range pool {
		if p != nil {
			accountsByID[p.ID] = p
		}
	}
	snapshotCache := &openAISnapshotCacheStub{snapshotAccounts: pool, accountsByID: accountsByID}
	groupRepo := &stubSchedulerGroupRepo{
		groupsByID: map[int64]*Group{
			groupID: {ID: groupID, Platform: groupPlatform},
		},
	}
	snapshotService := &SchedulerSnapshotService{cache: snapshotCache, groupRepo: groupRepo}
	repoAccounts := make([]Account, 0, len(pool))
	for _, p := range pool {
		if p != nil {
			repoAccounts = append(repoAccounts, *p)
		}
	}
	bindings := map[string]int64{}
	if sessionHash != "" && stickyAccountID > 0 {
		// openai_sticky_compat.openAISessionCacheKey prefixes "openai:" before
		// looking up the binding, regardless of group.platform.
		bindings["openai:"+sessionHash] = stickyAccountID
	}
	cache := &stubGatewayCache{sessionBindings: bindings}
	return &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: repoAccounts},
		cache:              cache,
		cfg:                &config.Config{},
		schedulerSnapshot:  snapshotService,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}
}

// US-013 AC-001: newapi group with a valid newapi sticky-bound account
// returns that account on subsequent requests (sticky cache HIT).
func TestUS013_Sticky_NewAPIGroup_HitsBoundAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(83001)
	stickyAccount := newAPIAccount(83101, 7)
	pool := []*Account{stickyAccount, newAPIAccount(83102, 5)}
	svc := newStickyFixture(t, groupID, PlatformNewAPI, pool, "session-hash-newapi-ok", 83101)

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session-hash-newapi-ok", "", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(83101), selection.Account.ID, "sticky session HIT must reuse the bound account")
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
}

// US-011 AC-005 / US-013 AC-002 (sticky drift defense, primary): a sticky
// binding that points at an account whose Platform no longer matches the
// group's platform MUST be invalidated and the request MUST fall back to
// load-balance — never silently route across platforms.
//
// Scenario: openai group has a stale sticky binding pointing at a newapi
// account ID (e.g. cache from before a platform reassignment). The newapi
// account is even still in the snapshot to maximize the temptation to
// silently use it.
func TestUS011_Sticky_FailsOver_WhenAccountChangedPlatform(t *testing.T) {
	ctx := context.Background()
	groupID := int64(83002)
	stickyDrifted := newAPIAccount(83201, 7) // pretend this account "used to be openai"
	openaiBackup := openAIAccount(83202, 5)
	pool := []*Account{stickyDrifted, openaiBackup}
	svc := newStickyFixture(t, groupID, PlatformOpenAI, pool, "session-hash-openai-drifted", 83201)

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session-hash-openai-drifted", "", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.NotEqual(t, int64(83201), selection.Account.ID, "MUST NOT route openai group to drifted newapi sticky binding (security)")
	require.Equal(t, PlatformOpenAI, selection.Account.Platform, "openai group sticky drift must fail over to an openai account")
	require.Equal(t, int64(83202), selection.Account.ID)
	// Layer should be load-balance — sticky was rejected by IsOpenAICompatPoolMember.
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickySessionHit)
	// And the stale binding must have been deleted (cache cleanup).
	require.Equal(t, 1, svc.cache.(*stubGatewayCache).deletedSessions["openai:session-hash-openai-drifted"],
		"stale sticky binding must be deleted on drift detection")
}

// US-013 AC-003: symmetric defense — newapi group with a sticky binding
// pointing at an openai account also fails over to load balance.
func TestUS013_Sticky_NewAPIGroup_FailsOver_WhenStickyAccountIsOpenAI(t *testing.T) {
	ctx := context.Background()
	groupID := int64(83003)
	stickyDrifted := openAIAccount(83301, 0)
	newapiBackup := newAPIAccount(83302, 5)
	pool := []*Account{stickyDrifted, newapiBackup}
	svc := newStickyFixture(t, groupID, PlatformNewAPI, pool, "session-hash-newapi-drifted", 83301)

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session-hash-newapi-drifted", "", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(83302), selection.Account.ID, "newapi group sticky drift must fail over to a newapi account")
	require.Equal(t, PlatformNewAPI, selection.Account.Platform)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 1, svc.cache.(*stubGatewayCache).deletedSessions["openai:session-hash-newapi-drifted"])
}

// US-013 AC-004: sticky-bound newapi account whose channel_type was reset to
// 0 (incomplete configuration) MUST also trigger drift failover. Bridge
// dispatch would crash without channel_type — the IsOpenAICompatPoolMember
// channel_type>0 guard is the cheapest preflight.
func TestUS013_Sticky_NewAPIGroup_FailsOver_WhenChannelTypeReset(t *testing.T) {
	ctx := context.Background()
	groupID := int64(83004)
	stickyBroken := newAPIAccount(83401, 0) // channel_type was reset
	newapiBackup := newAPIAccount(83402, 5)
	pool := []*Account{stickyBroken, newapiBackup}
	svc := newStickyFixture(t, groupID, PlatformNewAPI, pool, "session-hash-newapi-channel-zero", 83401)

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session-hash-newapi-channel-zero", "", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(83402), selection.Account.ID,
		"channel_type=0 sticky binding must be invalidated; only channel_type>0 newapi remains eligible")
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

// US-015 AC-003 (regression baseline): openai group with a valid openai
// sticky binding still hits — the design must not weaken sticky behavior
// for unaffected groups.
func TestUS015_Sticky_OpenAIGroup_HitPreserved(t *testing.T) {
	ctx := context.Background()
	groupID := int64(83005)
	stickyAccount := openAIAccount(83501, 0)
	pool := []*Account{stickyAccount, openAIAccount(83502, 5)}
	svc := newStickyFixture(t, groupID, PlatformOpenAI, pool, "session-hash-openai-ok", 83501)

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session-hash-openai-ok", "", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(83501), selection.Account.ID, "openai group sticky HIT must continue to work unchanged")
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
}

// Compile-time anchor: silence unused imports if a future refactor removes a case.
var _ = errors.New

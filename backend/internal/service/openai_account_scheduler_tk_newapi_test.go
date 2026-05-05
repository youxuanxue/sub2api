//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Tests for docs/approved/newapi-as-fifth-platform.md §3.1 U1-U4 (scheduler-tier).
// Covers the AC subset of US-008/US-011/US-012/US-015 that can be exercised at
// the scheduler boundary without standing up PostgreSQL — the truly end-to-end
// HTTP+PG+upstream cases (US-008/US-009/US-010 e2e) are the testcontainer
// follow-up remains tracked by the Draft US-008/009/010 stories.

// stubSchedulerGroupRepo is the minimal GroupRepository stub the
// SchedulerSnapshotService.GetGroupByID path needs. It returns Group rows
// keyed by ID so a test can wire {groupID -> groupPlatform} mapping.
type stubSchedulerGroupRepo struct {
	GroupRepository
	groupsByID map[int64]*Group
}

func (r *stubSchedulerGroupRepo) GetByID(ctx context.Context, id int64) (*Group, error) {
	if g, ok := r.groupsByID[id]; ok {
		return g, nil
	}
	return nil, errors.New("group not found")
}

// newAPISchedFixture builds an OpenAIGatewayService wired with stubs for
// scheduler-tier tests. The pool argument is the snapshot the scheduler will
// see; pass mixed openai+newapi accounts to exercise the cross-platform
// safety filter, or a single-platform pool for happy-path coverage.
func newAPISchedFixture(t *testing.T, groupID int64, groupPlatform string, pool []*Account) (*OpenAIGatewayService, *defaultOpenAIAccountScheduler) {
	t.Helper()
	resetOpenAIAdvancedSchedulerSettingCacheForTest()

	accountsByID := make(map[int64]*Account, len(pool))
	for _, p := range pool {
		if p != nil {
			accountsByID[p.ID] = p
		}
	}
	snapshotCache := &openAISnapshotCacheStub{snapshotAccounts: pool, accountsByID: accountsByID, filterPlatform: groupPlatform}
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
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeStandard
	cfg.Gateway.Scheduling.LoadBatchEnabled = false // exercise selectAccountForModelWithExclusions path in unit tests

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: repoAccounts},
		cfg:                cfg,
		schedulerSnapshot:  snapshotService,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}
	sched := &defaultOpenAIAccountScheduler{service: svc}
	return svc, sched
}

func newAPIAccount(id int64, channelType int) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    50,
		ChannelType: channelType,
	}
}

func openAIAccount(id int64, priority int) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    priority,
	}
}

// US-008 AC-001 (scheduler tier): newapi group selects a newapi account when
// the pool contains a properly configured (channel_type>0) newapi account.
//
// Anchors design §3.1 U1+U2: groupPlatform is resolved to "newapi" via
// GetGroupByID, propagated into ScheduleRequest.GroupPlatform, and used by
// listSchedulableAccounts to fetch the newapi-bucketed pool.
func TestUS008_NewAPIGroup_Scheduler_PicksNewAPIAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(80001)
	pool := []*Account{newAPIAccount(80101, 7)}
	svc, _ := newAPISchedFixture(t, groupID, PlatformNewAPI, pool)

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(80101), selection.Account.ID, "newapi group must select the newapi account")
	require.Equal(t, PlatformNewAPI, selection.Account.Platform, "selected account platform must be newapi (no cross-platform leak)")
	require.Greater(t, selection.Account.ChannelType, 0, "selected newapi account must have channel_type>0 (bridge dispatch invariant)")
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

// US-008 AC-002 / US-012 AC-001 (scheduler tier): newapi group with an empty
// candidate pool MUST return an error (no fallback to openai accounts).
//
// This is the "newapi pool empty → clear error" defense from design §0.
// Note the stubSchedulerGroupRepo returns Group{Platform: newapi} so
// resolveGroupPlatform/listSchedulableAccounts both see "newapi" and the
// repo stub will yield zero rows for that platform.
func TestUS008_NewAPIGroup_PoolEmpty_NoFallback(t *testing.T) {
	ctx := context.Background()
	groupID := int64(80002)
	// Pool is intentionally empty for newapi platform; openai account exists
	// in repo but MUST NOT be selected for a newapi group.
	svc, _ := newAPISchedFixture(t, groupID, PlatformNewAPI, []*Account{openAIAccount(80201, 0)})

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.Error(t, err, "empty newapi pool must surface an error, never silently fall back to openai")
	require.True(t, selection == nil || selection.Account == nil, "no account may be selected when newapi pool is empty")
	msg := strings.ToLower(err.Error())
	require.True(t,
		strings.Contains(msg, "no available") &&
			(strings.Contains(msg, "newapi") || strings.Contains(msg, "openai") || strings.Contains(msg, "accounts")),
		"error message should clearly say no available accounts, got: %v", err)
}

// US-008 AC-003 / US-015 AC-001 (regression baseline): an openai group with
// only openai accounts in the pool must continue selecting an openai account.
// This is the "do no harm" guarantee for the existing openai surface.
func TestUS008_OpenAIGroup_SchedulerSelect_Unchanged(t *testing.T) {
	ctx := context.Background()
	groupID := int64(80003)
	pool := []*Account{openAIAccount(80301, 0), openAIAccount(80302, 5)}
	svc, _ := newAPISchedFixture(t, groupID, PlatformOpenAI, pool)

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, PlatformOpenAI, selection.Account.Platform, "openai group must continue selecting openai accounts")
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
}

// US-011 AC-004 (security: cross-platform pool poisoning).
//
// Construct an openai group whose snapshot pool was poisoned with a newapi
// account (e.g. by a stale cache entry or a misconfigured admin import). The
// scheduler MUST filter it out via IsOpenAICompatPoolMember and fall back to
// the legitimate openai candidate. This is the design §3.1 U3 invariant —
// the very bug §0 says "would silently send an openai request to a newapi
// account" if the filter were ever weakened.
func TestUS011_LoadBalance_FiltersOutNewAPIFromOpenAIGroup(t *testing.T) {
	ctx := context.Background()
	groupID := int64(80004)
	openaiOK := openAIAccount(80401, 0)
	newapiPoison := newAPIAccount(80402, 7)
	// Snapshot intentionally mixes platforms — this is the poisoning attack
	// surface IsOpenAICompatPoolMember(req.GroupPlatform) is supposed to plug.
	pool := []*Account{openaiOK, newapiPoison}
	svc, _ := newAPISchedFixture(t, groupID, PlatformOpenAI, pool)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(80401), selection.Account.ID, "must select the legitimate openai account, not the poisoned newapi one")
	require.Equal(t, PlatformOpenAI, selection.Account.Platform,
		"openai group MUST NEVER receive a newapi account — IsOpenAICompatPoolMember invariant")
}

// US-011 AC-004 reverse direction: a newapi group with an openai account
// poisoned into its pool must filter the openai account out and pick the
// newapi one. Symmetric defense — the rule is "strict equality", not
// "openai is privileged".
func TestUS011_LoadBalance_FiltersOutOpenAIFromNewAPIGroup(t *testing.T) {
	ctx := context.Background()
	groupID := int64(80005)
	newapiOK := newAPIAccount(80501, 7)
	openaiPoison := openAIAccount(80502, 0)
	pool := []*Account{newapiOK, openaiPoison}
	svc, _ := newAPISchedFixture(t, groupID, PlatformNewAPI, pool)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(80501), selection.Account.ID, "must select the legitimate newapi account, not the poisoned openai one")
	require.Equal(t, PlatformNewAPI, selection.Account.Platform)
}

// US-012 AC-002 (channel_type=0 in pool): even if a newapi account with
// channel_type=0 sneaks into the snapshot, it MUST be excluded — bridge
// dispatch would crash without a channel target. The channel_type>0 guard
// in IsOpenAICompatPoolMember is the cheapest preflight defense.
func TestUS012_LoadBalance_ExcludesNewAPIChannelTypeZero(t *testing.T) {
	ctx := context.Background()
	groupID := int64(80006)
	bad := newAPIAccount(80601, 0)  // channel_type=0 — incomplete config
	good := newAPIAccount(80602, 5) // legitimate newapi account
	pool := []*Account{bad, good}
	svc, _ := newAPISchedFixture(t, groupID, PlatformNewAPI, pool)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(80602), selection.Account.ID, "channel_type=0 newapi account must be excluded; only the channel_type>0 one is eligible")
}

// US-012 AC-003: newapi pool with ONLY a channel_type=0 account is
// effectively empty — must fail with the "no available accounts" error,
// not silently return the misconfigured account.
func TestUS012_NewAPIGroup_AllChannelTypeZero_PoolEmpty(t *testing.T) {
	ctx := context.Background()
	groupID := int64(80007)
	bad := newAPIAccount(80701, 0)
	pool := []*Account{bad}
	svc, _ := newAPISchedFixture(t, groupID, PlatformNewAPI, pool)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.Error(t, err, "all-channel_type=0 newapi pool must be treated as empty")
	require.True(t, selection == nil || selection.Account == nil)
}

// US-015 AC-002 / design §2.4 (cache key partitioning): the SchedulerBucket
// platform field MUST equal the resolved groupPlatform — so an openai bucket
// and a newapi bucket coexist in the same cache without collision. This pins
// the "no migration needed, openai bucket stays warm after upgrade" claim.
func TestUS015_SchedulerBucket_PartitionedByPlatform(t *testing.T) {
	groupID := int64(80008)
	gpid := &groupID
	openaiBucket := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	newapiBucket := SchedulerBucket{GroupID: groupID, Platform: PlatformNewAPI, Mode: SchedulerModeSingle}
	require.NotEqual(t, openaiBucket.String(), newapiBucket.String(), "openai and newapi buckets for the same group MUST hash to different cache keys")
	require.Contains(t, openaiBucket.String(), PlatformOpenAI)
	require.Contains(t, newapiBucket.String(), PlatformNewAPI)
	// Defensive: bucketFor uses platform verbatim; if a future refactor
	// canonicalizes platform names this test is the early warning.
	snap := &SchedulerSnapshotService{}
	got := snap.bucketFor(gpid, PlatformNewAPI, SchedulerModeSingle)
	require.Equal(t, PlatformNewAPI, got.Platform)
	require.Equal(t, groupID, got.GroupID)
}

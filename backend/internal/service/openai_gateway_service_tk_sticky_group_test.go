//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests for upstream Wei-Shaw/sub2api#1934 — sticky bindings must be invalidated
// when the bound account has drifted out of the group (group switch / removed
// from group), instead of continuing to route the group's traffic at a stale,
// out-of-group account. Covers all three sticky fast paths:
//   - legacy tryStickySessionHit (SelectAccountWithLoadAwareness, LoadBatch off)
//   - Layer-1 inline block        (SelectAccountWithLoadAwareness, LoadBatch on)
//   - scheduler selectBySessionHash (SelectAccountWithScheduler)
//
// Reuses newStickyFixture and the newAPIAccount/openAIAccount helpers from the
// fifth-platform sticky tests (openai_gateway_service_tk_newapi_pool_test.go).

func withGroups(a *Account, groupIDs ...int64) *Account {
	a.AccountGroups = a.AccountGroups[:0]
	a.GroupIDs = a.GroupIDs[:0]
	for _, gid := range groupIDs {
		a.AccountGroups = append(a.AccountGroups, AccountGroup{AccountID: a.ID, GroupID: gid})
		a.GroupIDs = append(a.GroupIDs, gid)
	}
	return a
}

func TestOpenaiStickyAccountStillInGroup(t *testing.T) {
	cases := []struct {
		name    string
		account *Account
		groupID int64
		want    bool
	}{
		{"nil account", nil, 5, false},
		{"non-positive group keeps", &Account{ID: 1}, 0, true},
		{"empty membership is conservative keep", &Account{ID: 1}, 5, true},
		{"member via AccountGroups", &Account{ID: 1, AccountGroups: []AccountGroup{{GroupID: 5}}}, 5, true},
		{"member via GroupIDs", &Account{ID: 1, GroupIDs: []int64{5}}, 5, true},
		{"known but drifted out", &Account{ID: 1, AccountGroups: []AccountGroup{{GroupID: 9}}}, 5, false},
		{"known via GroupIDs but drifted out", &Account{ID: 1, GroupIDs: []int64{9}}, 5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, openaiStickyAccountStillInGroup(tc.account, tc.groupID))
		})
	}
}

// Legacy path (LoadBatch off → tryStickySessionHit): sticky binding whose bound
// account now belongs to a different group must be invalidated and fail over.
func TestUS1934_Sticky_GroupDrift_ClearsBinding_Legacy(t *testing.T) {
	ctx := context.Background()
	groupID := int64(91001)
	otherGroup := int64(99999)
	// Same platform (newapi) so IsOpenAICompatPoolMember passes; only the group
	// membership differs — that is what must now be caught.
	stickyDrifted := withGroups(newAPIAccount(91101, 7), otherGroup)
	stickyDrifted.Priority = 90
	backup := withGroups(newAPIAccount(91102, 5), groupID)
	backup.Priority = 1 // deterministically wins Layer-2 over the drifted candidate
	pool := []*Account{stickyDrifted, backup}
	sessionHash := "sticky-group-drift-legacy"
	svc := newStickyFixture(t, groupID, PlatformNewAPI, pool, sessionHash, stickyDrifted.ID)

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, backup.ID, selection.Account.ID,
		"request must fail over to an in-group account, not the drifted sticky binding")

	cache := svc.cache.(*stubGatewayCache)
	require.GreaterOrEqual(t, cache.deletedSessions["openai:"+sessionHash], 1,
		"#1934: out-of-group sticky binding must be deleted")
}

// Scheduler path (SelectAccountWithScheduler → selectBySessionHash).
func TestUS1934_Sticky_GroupDrift_ClearsBinding_Scheduler(t *testing.T) {
	ctx := context.Background()
	groupID := int64(91002)
	otherGroup := int64(99998)
	stickyDrifted := withGroups(newAPIAccount(91201, 7), otherGroup)
	stickyDrifted.Priority = 90
	backup := withGroups(newAPIAccount(91202, 5), groupID)
	backup.Priority = 1 // deterministically wins Layer-2 over the drifted candidate
	pool := []*Account{stickyDrifted, backup}
	sessionHash := "sticky-group-drift-scheduler"
	svc := newStickyFixture(t, groupID, PlatformNewAPI, pool, sessionHash, stickyDrifted.ID)

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", sessionHash, "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, backup.ID, selection.Account.ID,
		"#1934: drifted sticky must fail over to load-balance in-group account")
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickySessionHit)
	require.GreaterOrEqual(t, svc.cache.(*stubGatewayCache).deletedSessions["openai:"+sessionHash], 1)
}

// Layer-1 inline path (LoadBatch ON → the SelectAccountWithLoadAwareness inline
// sticky block, which is the production default schedulingConfig). Mirrors the
// legacy/scheduler drift tests above so all THREE sticky fast paths are covered.
func TestUS1934_Sticky_GroupDrift_ClearsBinding_LoadBatchInline(t *testing.T) {
	ctx := context.Background()
	groupID := int64(91005)
	otherGroup := int64(99997)
	stickyDrifted := withGroups(newAPIAccount(91501, 7), otherGroup)
	stickyDrifted.Priority = 90
	backup := withGroups(newAPIAccount(91502, 5), groupID)
	backup.Priority = 1
	pool := []*Account{stickyDrifted, backup}
	sessionHash := "sticky-group-drift-loadbatch"
	svc := newStickyFixture(t, groupID, PlatformNewAPI, pool, sessionHash, stickyDrifted.ID)
	svc.cfg.Gateway.Scheduling.LoadBatchEnabled = true // force the Layer-1 inline block

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, backup.ID, selection.Account.ID,
		"#1934: Layer-1 inline path must fail over to an in-group account")
	require.GreaterOrEqual(t, svc.cache.(*stubGatewayCache).deletedSessions["openai:"+sessionHash], 1,
		"#1934: Layer-1 inline path must delete the out-of-group sticky binding")
}

// Regression guard A: a sticky account that IS a member of the group keeps the
// binding (HIT preserved).
func TestUS1934_Sticky_InGroup_HitPreserved(t *testing.T) {
	ctx := context.Background()
	groupID := int64(91003)
	stickyHealthy := withGroups(newAPIAccount(91301, 7), groupID)
	pool := []*Account{stickyHealthy, withGroups(newAPIAccount(91302, 5), groupID)}
	sessionHash := "sticky-in-group-healthy"
	svc := newStickyFixture(t, groupID, PlatformNewAPI, pool, sessionHash, stickyHealthy.ID)

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, stickyHealthy.ID, selection.Account.ID, "in-group sticky must continue to HIT")
	require.Equal(t, 0, svc.cache.(*stubGatewayCache).deletedSessions["openai:"+sessionHash],
		"#1934 regression guard: in-group sticky binding must NOT be cleared")
}

// Regression guard B: an account with UNKNOWN (empty) group membership keeps the
// binding — the scheduler snapshot serves group-filtered accounts that may not
// carry AccountGroups, so an empty set must not falsely clear a healthy session.
func TestUS1934_Sticky_EmptyGroups_HitPreserved(t *testing.T) {
	ctx := context.Background()
	groupID := int64(91004)
	stickyHealthy := newAPIAccount(91401, 7) // no AccountGroups set
	pool := []*Account{stickyHealthy, newAPIAccount(91402, 5)}
	sessionHash := "sticky-empty-groups"
	svc := newStickyFixture(t, groupID, PlatformNewAPI, pool, sessionHash, stickyHealthy.ID)

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, sessionHash, "", nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, stickyHealthy.ID, selection.Account.ID,
		"empty-membership sticky must be treated conservatively (kept), preserving snapshot contract")
	require.Equal(t, 0, svc.cache.(*stubGatewayCache).deletedSessions["openai:"+sessionHash])
}

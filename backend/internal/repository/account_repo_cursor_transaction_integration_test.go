//go:build integration

package repository

import (
	"context"
	"fmt"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	entgroup "github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCursorCreateAndGroupBindShareOuterTransaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil, nil)
	group, err := client.Group.Create().SetName(fmt.Sprintf("cursor-tx-%d", time.Now().UnixNano())).SetPlatform(service.PlatformNewAPI).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id=$1", group.ID)
	})
	for _, commit := range []bool{false, true} {
		tx, err := client.Tx(ctx)
		require.NoError(t, err)
		txCtx := dbent.NewTxContext(ctx, tx)
		_, err = tx.Group.Query().Where(entgroup.IDEQ(group.ID)).ForUpdate().Only(txCtx)
		require.NoError(t, err)
		account := &service.Account{Name: fmt.Sprintf("cursor-account-%d", time.Now().UnixNano()), Platform: service.PlatformNewAPI, Type: service.AccountTypeAPIKey, ChannelType: 14, Status: service.StatusActive, Extra: map[string]any{service.CursorSourceExtraKey: "cursor"}, Credentials: map[string]any{"api_key": "test-only", "base_url": "https://agentn.global.api5.cursor.sh", "api_base_urls": map[string]any{"anthropic": "https://agentn.global.api5.cursor.sh"}, "protocol_endpoints_exclusive": true}}
		require.NoError(t, repo.Create(txCtx, account))
		require.NoError(t, repo.BindGroups(txCtx, account.ID, []int64{group.ID}))
		loaded, err := repo.GetByID(txCtx, account.ID)
		require.NoError(t, err)
		require.Equal(t, []int64{group.ID}, loaded.GroupIDs)
		if commit {
			require.NoError(t, tx.Commit())
		} else {
			require.NoError(t, tx.Rollback())
		}
		var count int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE id=$1", account.ID).Scan(&count))
		if commit {
			require.Equal(t, 1, count)
		} else {
			require.Zero(t, count)
		}
		t.Cleanup(func() {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id=$1", account.ID)
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id=$1", account.ID)
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", account.ID)
		})
	}
}

func TestCursorReconnectPreservesPersistedPause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil, nil)
	groups := newGroupRepositoryWithSQL(client, integrationDB)
	admin := service.NewAdminService(nil, groups, repo, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	group, err := client.Group.Create().SetName(fmt.Sprintf("cursor-renew-%d", time.Now().UnixNano())).SetPlatform(service.PlatformNewAPI).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id=$1", group.ID)
	})
	for _, paused := range []bool{false, true} {
		past := time.Now().Add(-time.Hour)
		account := &service.Account{Name: fmt.Sprintf("cursor-renew-%d", time.Now().UnixNano()),
			Platform: service.PlatformNewAPI, Type: service.AccountTypeAPIKey, ChannelType: 14,
			Status: service.StatusActive, Schedulable: !paused, AutoPauseOnExpired: true, ExpiresAt: &past,
			Extra:       map[string]any{service.CursorSourceExtraKey: "cursor"},
			Credentials: map[string]any{"api_key": "test-only", "base_url": "https://agentn.global.api5.cursor.sh", "api_base_urls": map[string]any{"anthropic": "https://agentn.global.api5.cursor.sh"}, "protocol_endpoints_exclusive": true}}
		require.NoError(t, repo.Create(ctx, account))
		ids := []int64{group.ID}
		require.NoError(t, repo.BindGroups(ctx, account.ID, ids))
		future := time.Now().Add(time.Hour).Unix()
		updated, err := admin.SaveCursorAccount(ctx, nil, &service.UpdateAccountInput{ExpiresAt: &future, GroupIDs: &ids}, account.ID)
		require.NoError(t, err)
		require.Equal(t, !paused, updated.Schedulable)
		require.Equal(t, !paused, updated.IsSchedulable())
		loaded, err := repo.GetByID(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, !paused, loaded.Schedulable)
		require.Equal(t, future, loaded.ExpiresAt.Unix())
		// Free the dedicated group before exercising the other pause state.
		require.NoError(t, repo.BindGroups(ctx, account.ID, nil))
		t.Cleanup(func() {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id=$1", account.ID)
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", account.ID)
		})
	}
}

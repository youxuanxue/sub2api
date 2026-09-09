//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCursorRefreshCandidateAndAtomicExpiry(t *testing.T) {
	ctx := t.Context()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil, nil)
	past := time.Now().Add(-time.Hour)
	create := func(marker bool, schedulable bool) *service.Account {
		a := &service.Account{Name: fmt.Sprintf("cursor-refresh-%d", time.Now().UnixNano()), Platform: service.PlatformNewAPI, Type: service.AccountTypeAPIKey, ChannelType: 14, Status: service.StatusActive, Schedulable: schedulable, ExpiresAt: &past,
			Credentials: map[string]any{"api_key": "old-test-token", "base_url": "https://agentn.global.api5.cursor.sh"}, Extra: map[string]any{}}
		if marker {
			a.Extra[service.CursorSourceExtraKey] = "cursor"
		}
		require.NoError(t, repo.Create(ctx, a))
		t.Cleanup(func() {
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id=$1", a.ID)
			_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", a.ID)
		})
		return a
	}
	active := create(true, true)
	ordinary := create(false, true)
	paused := create(true, false)
	page, err := repo.ListOAuthRefreshCandidatePage(ctx, service.OAuthRefreshPageOptions{Platforms: []string{service.PlatformNewAPI}, AfterID: active.ID - 1, Limit: 100, ActiveOnly: true, IncludeSetupToken: true, RequireRefreshToken: true})
	require.NoError(t, err)
	ids := []int64{}
	for _, a := range page.Accounts {
		ids = append(ids, a.ID)
	}
	require.Contains(t, ids, active.ID, "legacy Cursor access token is also its renewal grant")
	require.NotContains(t, ids, ordinary.ID)
	require.NotContains(t, ids, paused.ID)

	fresh, err := repo.GetByID(ctx, active.ID)
	require.NoError(t, err)
	expected := fresh.Credentials
	updated := make(map[string]any, len(expected))
	for k, v := range expected {
		updated[k] = v
	}
	updated["api_key"] = "new-test-token"
	expires := time.Now().Add(60 * 24 * time.Hour).Truncate(time.Second)
	// A pause during the network request must survive successful renewal.
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET schedulable=false WHERE id=$1", active.ID)
	require.NoError(t, err)
	applied, err := repo.UpdateCursorOAuthCredentialsIfUnchanged(ctx, active.ID, expected, fresh.ProxyID, updated, expires)
	require.NoError(t, err)
	require.True(t, applied)
	fresh, err = repo.GetByID(ctx, active.ID)
	require.NoError(t, err)
	require.False(t, fresh.Schedulable)
	require.Equal(t, expires.Unix(), fresh.ExpiresAt.Unix())
	require.Equal(t, "new-test-token", fresh.GetCredential("api_key"))
	var outbox int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", active.ID).Scan(&outbox))
	require.Positive(t, outbox)

	// Stale success and failure may neither overwrite nor quarantine new credentials.
	applied, err = repo.UpdateCursorOAuthCredentialsIfUnchanged(ctx, active.ID, expected, fresh.ProxyID, expected, past)
	require.NoError(t, err)
	require.False(t, applied)
	applied, err = repo.SetCursorOAuthRefreshFailureIfUnchanged(ctx, active.ID, expected, fresh.ProxyID, "revoked", nil)
	require.NoError(t, err)
	require.False(t, applied)
	applied, err = repo.SetCursorOAuthRefreshFailureIfUnchanged(ctx, active.ID, updated, fresh.ProxyID, "revoked", nil)
	require.NoError(t, err)
	require.False(t, applied, "paused accounts must not be quarantined")
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET schedulable=true WHERE id=$1", active.ID)
	require.NoError(t, err)
	applied, err = repo.SetCursorOAuthRefreshFailureIfUnchanged(ctx, active.ID, updated, fresh.ProxyID, "revoked", nil)
	require.NoError(t, err)
	require.True(t, applied)
	fresh, err = repo.GetByID(ctx, active.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusError, fresh.Status)
}

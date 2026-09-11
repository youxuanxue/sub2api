//go:build integration

package repository

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCandidateSupplierFaultPersistentSharingAndRecovery(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil, nil)
	credential := uuid.NewString()
	create := func(channel int, key string) *service.Account {
		return mustCreateAccount(t, client, &service.Account{Name: "candidate-supplier", Platform: service.PlatformNewAPI,
			Type: service.AccountTypeAPIKey, ChannelType: channel, Concurrency: channel + 3,
			Credentials: map[string]any{"base_url": "https://supplier.example/v1", "api_key": key,
				"model_mapping": map[string]any{"request-alias": "upstream-model"}},
			Extra: map[string]any{service.SupplierSourceIDExtraKey: float64(channel), "model_rate_limits": map[string]any{"upstream-model": "preserved"}},
		})
	}
	first, peer, other := create(1, credential), create(14, credential), create(14, uuid.NewString())
	require.NoError(t, client.Account.UpdateOneID(peer.ID).SetSchedulable(false).Exec(ctx))
	peer.Schedulable = false
	cfg := &config.Config{}
	cfg.Totp.EncryptionKey = "isolated-supplier-test"
	svc := service.NewRateLimitService(repo, nil, cfg, nil, nil)
	require.True(t, svc.HandleUpstreamError(ctx, first, http.StatusUnauthorized, nil,
		[]byte(`{"error":{"code":"invalid_api_key","message":"Invalid API key"}}`)))
	read := func(id int64) *service.Account { a, err := repo.GetByID(ctx, id); require.NoError(t, err); return a }
	for _, before := range []*service.Account{first, peer} {
		after := read(before.ID)
		require.Equal(t, service.StatusError, after.Status)
		require.True(t, service.IsSupplierCredentialFault(after.ErrorMessage))
		require.Equal(t, before.Schedulable, after.Schedulable)
		require.Equal(t, before.Concurrency, after.Concurrency)
		require.Equal(t, before.Credentials, after.Credentials)
		require.Equal(t, before.Extra, after.Extra)
	}
	require.Equal(t, service.StatusActive, read(other.ID).Status)
	failed := read(first.ID)
	changed, err := repo.UpdateSupplierCredentialFault(ctx, failed, "")
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, svc.RecoverSupplierCredentialPeers(ctx, failed))
	require.Equal(t, service.StatusActive, read(peer.ID).Status)
	require.False(t, read(peer.ID).Schedulable, "recovery must preserve manual pause")
	var events int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id IN ($1,$2)", first.ID, peer.ID).Scan(&events))
	require.Equal(t, 4, events, "each successful fault/recovery write publishes one invalidation")
	// A concurrent credential rotation must defeat a stale fault write without outbox noise.
	stale := read(peer.ID)
	rotated := map[string]any{"base_url": "https://supplier.example/v1", "api_key": uuid.NewString()}
	require.NoError(t, client.Account.UpdateOneID(peer.ID).SetCredentials(rotated).Exec(ctx))
	changed, err = repo.UpdateSupplierCredentialFault(ctx, stale, "Supplier credential failure: invalid key")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, service.StatusActive, read(peer.ID).Status)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id IN ($1,$2)", first.ID, peer.ID).Scan(&events))
	require.Equal(t, 4, events)
}

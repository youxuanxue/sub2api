//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/accountgroup"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func insertCapabilityBatchFixture(t testing.TB, tx *sql.Tx, accounts, endpoints int) []int64 {
	t.Helper()
	ctx := context.Background()
	caps := make([]int64, endpoints)
	for i := range caps {
		account := &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"base_url": fmt.Sprintf("https://batch-%d.example.test/v1", i), "api_key": "fixture"}}
		identity, governed, err := service.BuildProtocolEndpointIdentity(account)
		require.NoError(t, err)
		require.True(t, governed)
		raw, err := identity.CanonicalJSON()
		require.NoError(t, err)
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO protocol_endpoint_capabilities (capability_key,identity,supported_protocols,probe_evidence,revision)
VALUES ($1,$2,'["responses"]'::jsonb,$3,7) RETURNING id`, identity.Key(), string(raw), capabilityBatchEvidence(32)).Scan(&caps[i]))
	}
	ids := make([]int64, accounts)
	for i := range ids {
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO accounts
(name,platform,type,credentials,extra,concurrency,priority,rate_multiplier,status,schedulable,auto_pause_on_expired,channel_type,quota_dimension,protocol_endpoint_capability_id,created_at,updated_at)
VALUES ($1,'openai','api_key','{}'::jsonb,'{}'::jsonb,1,1,1,'active',true,false,0,'global',$2,NOW(),NOW()) RETURNING id`, fmt.Sprintf("batch-account-%d", i), caps[i%endpoints]).Scan(&ids[i]))
	}
	return ids
}

func TestAccountCapabilityBatchMatchesSingleReadsAndRefreshes(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	ids := insertCapabilityBatchFixture(t, tx, 6, 2)
	repo := &accountRepository{sql: tx}
	capRepo := newProtocolEndpointCapabilityRepositoryWithDB(tx)
	// The linked count includes unrequested/disabled accounts, excludes soft deletes.
	_, err := tx.ExecContext(ctx, `UPDATE accounts SET status='disabled',schedulable=false WHERE id=$1`, ids[2])
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, ids[4])
	require.NoError(t, err)
	requested := []int64{ids[0], ids[1], ids[2], ids[4], ids[0], -1, 0}
	batch, err := repo.loadProtocolEndpointCapabilities(ctx, requested)
	require.NoError(t, err)
	require.Len(t, batch, 3)
	require.NotContains(t, batch, ids[3], "other accounts sharing an endpoint must not leak into the requested union")
	require.NotContains(t, batch, ids[4], "soft-deleted account must not be returned")
	for _, id := range ids[:3] {
		single, err := capRepo.GetByAccountID(ctx, id)
		require.NoError(t, err)
		// The single-read API does not populate LinkedAccountCount.
		got := *batch[id]
		count := got.LinkedAccountCount
		got.LinkedAccountCount = 0
		require.Equal(t, single, &got)
		if id == ids[1] {
			require.Equal(t, 3, count)
		} else {
			require.Equal(t, 2, count)
		}
	}
	require.Equal(t, batch[ids[0]], batch[ids[2]])
	batch[ids[0]].SupportedProtocols[0] = protocolrouter.ProtocolMessages
	require.Equal(t, protocolrouter.ProtocolResponses, batch[ids[2]].SupportedProtocols[0])
	// Refresh revision/evidence, relink one account and unlink another. The next
	// call sees all writes from this transaction without any decoded global cache.
	_, err = tx.ExecContext(ctx, `UPDATE protocol_endpoint_capabilities SET revision=8,supported_protocols='["messages"]'::jsonb,probe_evidence='{"initial_probe_completed":true,"verdicts":{"revision":8}}'::jsonb WHERE id=$1`, batch[ids[1]].ID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET protocol_endpoint_capability_id=$2 WHERE id=$1`, ids[0], batch[ids[1]].ID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET protocol_endpoint_capability_id=NULL WHERE id=$1`, ids[2])
	require.NoError(t, err)
	fresh, err := repo.loadProtocolEndpointCapabilities(ctx, requested)
	require.NoError(t, err)
	require.Len(t, fresh, 2)
	require.Equal(t, fresh[ids[0]], fresh[ids[1]])
	require.EqualValues(t, 8, fresh[ids[0]].Revision)
	require.Equal(t, []protocolrouter.Protocol{protocolrouter.ProtocolMessages}, fresh[ids[0]].SupportedProtocols)
	require.Equal(t, float64(8), fresh[ids[0]].ProbeEvidence.Verdicts["revision"])
	require.Equal(t, 4, fresh[ids[0]].LinkedAccountCount)
	raw, err := json.Marshal(fresh[ids[0]].Identity)
	require.NoError(t, err)
	require.Contains(t, string(raw), "batch-1.example.test")
}

// Includes actual PostgreSQL execution, wire transfer and Go materialization.
func BenchmarkAccountCapabilityBatchPostgres(b *testing.B) {
	for _, tc := range []struct {
		name                string
		accounts, endpoints int
	}{{"single", 1, 1}, {"shared_64", 64, 1}, {"distinct_64", 64, 64}} {
		b.Run(tc.name, func(b *testing.B) {
			tx, err := integrationDB.BeginTx(context.Background(), nil)
			require.NoError(b, err)
			defer func() { _ = tx.Rollback() }()
			ids := insertCapabilityBatchFixture(b, tx, tc.accounts, tc.endpoints)
			repo := &accountRepository{sql: tx}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got, err := repo.loadProtocolEndpointCapabilities(context.Background(), ids)
				if err != nil {
					b.Fatal(err)
				}
				if len(got) != len(ids) {
					b.Fatal("missing capability links")
				}
			}
			b.StopTimer()
		})
	}
}

func TestCandidateAccountBatchKeepsFreshMembershipAndRuntimeFacts(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil, nil)
	group := mustCreateGroup(t, client, &service.Group{Name: "batch-authorized", Platform: service.PlatformOpenAI})
	other := mustCreateGroup(t, client, &service.Group{Name: "batch-other", Platform: service.PlatformOpenAI})
	first := mustCreateAccount(t, client, &service.Account{Name: "batch-first", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-first", "base_url": "https://batch.example.test/v1", "model_mapping": map[string]any{"public-model": "upstream-model"}}})
	second := mustCreateAccount(t, client, &service.Account{Name: "batch-second", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-second", "base_url": "https://batch.example.test/v1"}})
	outside := mustCreateAccount(t, client, &service.Account{Name: "batch-outside", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-outside", "base_url": "https://batch.example.test/v1"}})
	for _, a := range []*service.Account{first, second, outside} {
		identity, governed, err := service.BuildProtocolEndpointIdentity(a)
		require.NoError(t, err)
		require.True(t, governed)
		_, err = repo.EnsureAccountLink(ctx, a, identity, []protocolrouter.Protocol{protocolrouter.ProtocolResponses}, false)
		require.NoError(t, err)
	}
	mustBindAccountToGroup(t, client, first.ID, group.ID, 1)
	mustBindAccountToGroup(t, client, second.ID, group.ID, 2)
	mustBindAccountToGroup(t, client, outside.ID, other.ID, 3)
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	_, err := client.Account.UpdateOneID(second.ID).SetStatus(service.StatusDisabled).SetSchedulable(false).SetRateLimitResetAt(until).Save(ctx)
	require.NoError(t, err)
	list, err := repo.ListCandidateAccounts(ctx, []int64{group.ID})
	require.NoError(t, err)
	byID := map[int64]service.Account{}
	for _, a := range list {
		byID[a.ID] = a
	}
	require.Len(t, byID, 2)
	require.NotContains(t, byID, outside.ID)
	require.Equal(t, service.StatusDisabled, byID[second.ID].Status, "support candidates retain disabled members")
	require.False(t, byID[second.ID].Schedulable)
	require.True(t, until.Equal(*byID[second.ID].RateLimitResetAt))
	mapping, ok := byID[first.ID].Credentials["model_mapping"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "upstream-model", mapping["public-model"])
	require.Equal(t, byID[first.ID].ProtocolEndpointCapability, byID[second.ID].ProtocolEndpointCapability)
	require.Equal(t, 3, byID[first.ID].ProtocolEndpointCapability.LinkedAccountCount)
	require.Equal(t, []int64{group.ID}, byID[first.ID].GroupIDs)
	require.Equal(t, group.Name, byID[first.ID].Groups[0].Name)
	require.Equal(t, 1, byID[first.ID].AccountGroups[0].Priority)
	_, err = client.Account.UpdateOneID(first.ID).SetCredentials(map[string]any{"api_key": "fixture-rotated", "base_url": "https://changed.example.test/v1", "model_mapping": map[string]any{"public-model": "updated-model"}}).Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Delete().Where(accountgroup.AccountIDEQ(second.ID), accountgroup.GroupIDEQ(group.ID)).Exec(ctx)
	require.NoError(t, err)
	fresh, err := repo.ListCandidateAccounts(ctx, []int64{group.ID})
	require.NoError(t, err)
	require.Len(t, fresh, 1)
	require.Equal(t, first.ID, fresh[0].ID)
	require.Equal(t, "fixture-rotated", fresh[0].Credentials["api_key"])
	require.Equal(t, "https://changed.example.test/v1", fresh[0].Credentials["base_url"])
	freshMapping, ok := fresh[0].Credentials["model_mapping"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "updated-model", freshMapping["public-model"])
}

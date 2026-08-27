//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositoryProtocolPublicationRollsBackWhenFinalReadinessRejectsCandidate(t *testing.T) {
	ctx := context.Background()
	setupTx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)

	accountID := insertProtocolCapabilityTestAccount(t, setupTx, "cap-final-readiness-rollback", "secret")
	legacyUpdatedAt := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	_, err = setupTx.ExecContext(ctx, `
UPDATE accounts
SET extra=jsonb_set(extra, '{supported_protocols}', '["messages"]'::jsonb, true),
    updated_at=$2
WHERE id=$1`, accountID, legacyUpdatedAt)
	require.NoError(t, err)

	account := &service.Account{
		ID:       accountID,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://final-readiness.example.test/v1",
		},
	}
	identity, governed, err := service.BuildProtocolEndpointIdentity(account)
	require.NoError(t, err)
	require.True(t, governed)
	capabilityRepo := newProtocolEndpointCapabilityRepositoryWithDB(setupTx)
	_, err = capabilityRepo.EnsureAccountLink(
		ctx,
		account,
		identity,
		[]protocolrouter.Protocol{protocolrouter.ProtocolResponses},
		false,
	)
	require.NoError(t, err)
	require.NoError(t, setupTx.Commit())

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = integrationDB.ExecContext(cleanupCtx, "DELETE FROM scheduler_outbox WHERE account_id=$1", accountID)
		_, _ = integrationDB.ExecContext(cleanupCtx, "DELETE FROM accounts WHERE id=$1", accountID)
		_, _ = integrationDB.ExecContext(cleanupCtx, "DELETE FROM protocol_endpoint_capabilities WHERE capability_key=$1", identity.Key())
	})

	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil, nil)
	finalReadinessRejected := errors.New("final readiness rejected candidate")
	_, err = repo.PublishProtocolRoutingProjections(ctx, func(txCtx context.Context) error {
		txRepo := repo.protocolCapabilityRepository(txCtx)
		var (
			projection  []byte
			outboxCount int
		)
		rows, queryErr := txRepo.db.QueryContext(txCtx, `
SELECT extra->'supported_protocols',
       (SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1)
FROM accounts
WHERE id=$1`, accountID)
		require.NoError(t, queryErr)
		defer func() { _ = rows.Close() }()
		require.True(t, rows.Next())
		require.NoError(t, rows.Scan(&projection, &outboxCount))
		require.JSONEq(t, `["responses"]`, string(projection), "final readiness must inspect published facts")
		require.Equal(t, 1, outboxCount)
		return finalReadinessRejected
	})
	require.ErrorIs(t, err, finalReadinessRejected)

	var (
		projection  []byte
		updatedAt   time.Time
		outboxCount int
	)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT extra->'supported_protocols', updated_at,
       (SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1)
FROM accounts
WHERE id=$1`, accountID).Scan(&projection, &updatedAt, &outboxCount))
	require.JSONEq(t, `["messages"]`, string(projection), "failed final readiness exposed candidate projection")
	require.True(t, updatedAt.Equal(legacyUpdatedAt), "failed final readiness changed account revision from %s to %s", legacyUpdatedAt, updatedAt)
	require.Zero(t, outboxCount, "failed final readiness committed scheduler invalidation")
}

func TestProtocolEndpointCapabilityRepository_PreparationDoesNotPublishLegacyState(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo := newProtocolEndpointCapabilityRepositoryWithDB(tx)

	accountID := insertProtocolCapabilityTestAccount(t, tx, "cap-preparation", "secret")
	legacyUpdatedAt := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	_, err := tx.ExecContext(ctx, `
UPDATE accounts
SET extra=jsonb_set(extra, '{supported_protocols}', '["messages"]'::jsonb, true),
    updated_at=$2
WHERE id=$1`, accountID, legacyUpdatedAt)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id=$1", accountID)
	require.NoError(t, err)

	account := &service.Account{
		ID:       accountID,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://relay.example.test/v1",
		},
	}
	identity, governed, err := service.BuildProtocolEndpointIdentity(account)
	require.NoError(t, err)
	require.True(t, governed)

	capability, err := repo.EnsureAccountLink(ctx, account, identity, []protocolrouter.Protocol{protocolrouter.ProtocolResponses}, false)
	require.NoError(t, err)
	require.NotNil(t, capability)

	var (
		linkedCapabilityID int64
		projection         []byte
		updatedAt          time.Time
		outboxCount        int
	)
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT protocol_endpoint_capability_id, extra->'supported_protocols', updated_at
FROM accounts
WHERE id=$1`, accountID).Scan(&linkedCapabilityID, &projection, &updatedAt))
	require.Equal(t, capability.ID, linkedCapabilityID)
	require.JSONEq(t, `["messages"]`, string(projection), "startup preparation must not publish the rollback projection")
	require.True(t, updatedAt.Equal(legacyUpdatedAt), "startup preparation changed account updated_at from %s to %s", legacyUpdatedAt, updatedAt)
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", accountID).Scan(&outboxCount))
	require.Zero(t, outboxCount, "startup preparation must not invalidate scheduler snapshots")

	lease, acquired, err := repo.AcquireProbeLease(ctx, identity.Key(), "startup-probe", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	prepared, affected, err := repo.CommitPreparedProbeResult(ctx, lease, service.ProtocolCapabilityMutation{
		SupportedProtocols:    []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions},
		InitialProbeCompleted: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, affected)
	require.Equal(t, []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions}, prepared.SupportedProtocols)
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT extra->'supported_protocols', updated_at
FROM accounts
WHERE id=$1`, accountID).Scan(&projection, &updatedAt))
	require.JSONEq(t, `["messages"]`, string(projection), "startup probe published the rollback projection")
	require.True(t, updatedAt.Equal(legacyUpdatedAt), "startup probe changed account updated_at from %s to %s", legacyUpdatedAt, updatedAt)
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", accountID).Scan(&outboxCount))
	require.Zero(t, outboxCount, "startup probe invalidated scheduler snapshots")

	affected, err = repo.PublishProtocolRoutingProjections(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, affected)
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT extra->'supported_protocols', updated_at
FROM accounts
WHERE id=$1`, accountID).Scan(&projection, &updatedAt))
	require.JSONEq(t, `["chat_completions"]`, string(projection))
	require.True(t, updatedAt.After(legacyUpdatedAt), "publication did not advance account revision")
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1", accountID).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)

	publishedUpdatedAt := updatedAt
	affected, err = repo.PublishProtocolRoutingProjections(ctx)
	require.NoError(t, err)
	require.Zero(t, affected, "unchanged rollback projection must not create revision or outbox churn")
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT updated_at,
       (SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1)
FROM accounts
WHERE id=$1`, accountID).Scan(&updatedAt, &outboxCount))
	require.True(t, updatedAt.Equal(publishedUpdatedAt), "idempotent publication changed account revision from %s to %s", publishedUpdatedAt, updatedAt)
	require.Equal(t, 1, outboxCount, "idempotent publication duplicated scheduler invalidation")
}

func TestProtocolEndpointCapabilityRepositorySharesOneRowAndPublishesEveryLinkedAccount(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo := newProtocolEndpointCapabilityRepositoryWithDB(tx)

	firstID := insertProtocolCapabilityTestAccount(t, tx, "cap-shared-1", "first-secret")
	secondID := insertProtocolCapabilityTestAccount(t, tx, "cap-shared-2", "second-secret")
	first := &service.Account{ID: firstID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "first-secret", "base_url": "https://relay.example.test/v1"}}
	second := &service.Account{ID: secondID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "second-secret", "base_url": "https://relay.example.test/v1"}}
	identity, governed, err := service.BuildProtocolEndpointIdentity(first)
	require.NoError(t, err)
	require.True(t, governed)

	firstCapability, err := repo.EnsureAccountLink(ctx, first, identity, []protocolrouter.Protocol{protocolrouter.ProtocolResponses}, false)
	require.NoError(t, err)
	secondCapability, err := repo.EnsureAccountLink(ctx, second, identity, []protocolrouter.Protocol{protocolrouter.ProtocolMessages}, false)
	require.NoError(t, err)
	require.Equal(t, firstCapability.ID, secondCapability.ID)
	require.Equal(t, firstCapability.CapabilityKey, secondCapability.CapabilityKey)
	require.ElementsMatch(t, []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses}, secondCapability.SupportedProtocols)

	for _, accountID := range []int64{firstID, secondID} {
		var capabilityID int64
		var projection sql.NullString
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT protocol_endpoint_capability_id, extra->>'supported_protocols' FROM accounts WHERE id=$1`, accountID).Scan(&capabilityID, &projection))
		require.Equal(t, firstCapability.ID, capabilityID)
		require.False(t, projection.Valid, "link preparation published legacy state")
	}

	affected, err := repo.PublishProtocolRoutingProjections(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, affected)
	for _, accountID := range []int64{firstID, secondID} {
		var projection []byte
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT extra->'supported_protocols' FROM accounts WHERE id=$1`, accountID).Scan(&projection))
		require.JSONEq(t, `["messages","responses"]`, string(projection))
		_, err = tx.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id=$1", accountID)
		require.NoError(t, err)
	}

	lease, acquired, err := repo.AcquireProbeLease(ctx, identity.Key(), "shared-probe", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	updated, affected, err := repo.CommitProbeResult(ctx, lease, service.ProtocolCapabilityMutation{
		SupportedProtocols:    []protocolrouter.Protocol{protocolrouter.ProtocolResponses},
		InitialProbeCompleted: true,
	})
	require.NoError(t, err)
	require.Equal(t, 2, affected)
	require.Equal(t, []protocolrouter.Protocol{protocolrouter.ProtocolResponses}, updated.SupportedProtocols)
	for _, accountID := range []int64{firstID, secondID} {
		var outboxCount int
		require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM scheduler_outbox
WHERE event_type=$1 AND account_id=$2`, service.SchedulerOutboxEventAccountChanged, accountID).Scan(&outboxCount))
		require.Equal(t, 1, outboxCount, "capability commit must invalidate every linked scheduler snapshot")
	}
}

func TestProtocolEndpointCapabilityRepositoryLeaseAndCASRejectStaleWriter(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	repo := newProtocolEndpointCapabilityRepositoryWithDB(tx)
	accountID := insertProtocolCapabilityTestAccount(t, tx, "cap-lease", "secret")
	account := &service.Account{ID: accountID, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "secret", "base_url": "https://relay.example.test/v1"}}
	identity, governed, err := service.BuildProtocolEndpointIdentity(account)
	require.NoError(t, err)
	require.True(t, governed)
	_, err = repo.EnsureAccountLink(ctx, account, identity, nil, false)
	require.NoError(t, err)

	lease, acquired, err := repo.AcquireProbeLease(ctx, identity.Key(), "worker-a", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, acquired, err = repo.AcquireProbeLease(ctx, identity.Key(), "worker-b", time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.False(t, acquired)

	stale := lease
	stale.Revision--
	_, _, err = repo.CommitProbeResult(ctx, stale, service.ProtocolCapabilityMutation{SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolResponses}, InitialProbeCompleted: true})
	require.ErrorIs(t, err, service.ErrProtocolCapabilityStaleWrite)

	updated, affected, err := repo.CommitProbeResult(ctx, lease, service.ProtocolCapabilityMutation{SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolResponses}, InitialProbeCompleted: true})
	require.NoError(t, err)
	require.Equal(t, 1, affected)
	require.Equal(t, int64(2), updated.Revision)
	_, _, err = repo.CommitProbeResult(ctx, lease, service.ProtocolCapabilityMutation{SupportedProtocols: nil, InitialProbeCompleted: true})
	require.ErrorIs(t, err, service.ErrProtocolCapabilityStaleWrite)
}

func TestAccountRepositoryLifecycleLinksAndRelinksProtocolEndpointCapability(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil, nil)
	account := &service.Account{
		Name:        "capability-lifecycle",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "first-secret", "base_url": "https://relay.example.test/v1"},
		Extra:       map[string]any{},
		Concurrency: 1,
		Priority:    1,
		Status:      service.StatusActive,
		Schedulable: true,
	}
	require.NoError(t, repo.Create(ctx, account))

	created, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, created.ProtocolEndpointCapabilityID)
	require.NotNil(t, created.ProtocolEndpointCapability)
	assertAccountProtocolProjectionAndSingleOutbox(t, tx, created)
	clearAccountProtocolOutbox(t, tx, account.ID)
	firstCapabilityID := *created.ProtocolEndpointCapabilityID
	firstCapabilityKey := created.ProtocolEndpointCapability.CapabilityKey

	created.Credentials["api_key"] = "rotated-secret"
	require.NoError(t, repo.Update(ctx, created))
	rotated, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, firstCapabilityID, *rotated.ProtocolEndpointCapabilityID)
	require.Equal(t, firstCapabilityKey, rotated.ProtocolEndpointCapability.CapabilityKey)
	assertAccountProtocolProjectionAndSingleOutbox(t, tx, rotated)
	clearAccountProtocolOutbox(t, tx, account.ID)

	rotated.Credentials["base_url"] = "https://other.example.test/v1"
	require.NoError(t, repo.Update(ctx, rotated))
	relinked, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotEqual(t, firstCapabilityID, *relinked.ProtocolEndpointCapabilityID)
	require.NotEqual(t, firstCapabilityKey, relinked.ProtocolEndpointCapability.CapabilityKey)
	assertAccountProtocolProjectionAndSingleOutbox(t, tx, relinked)
	clearAccountProtocolOutbox(t, tx, account.ID)

	relinked.Type = service.AccountTypeServiceAccount
	require.NoError(t, repo.Update(ctx, relinked))
	ungoverned, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Nil(t, ungoverned.ProtocolEndpointCapabilityID)
	require.Nil(t, ungoverned.ProtocolEndpointCapability)
	assertAccountLegacyProjectionAndSingleOutbox(t, tx, account.ID, nil)
}

func TestAccountRepositoryUpdateCredentialsRelinksIdentityButRetainsTokenRotation(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil, nil)
	account := &service.Account{
		Name:        "antigravity-credential-lifecycle",
		Platform:    service.PlatformAntigravity,
		Type:        service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "first-token", "project_id": "project-a", "plan_type": "free"},
		Extra:       map[string]any{},
		Concurrency: 1,
		Priority:    1,
		Status:      service.StatusActive,
		Schedulable: true,
	}
	require.NoError(t, repo.Create(ctx, account))
	created, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, created.ProtocolEndpointCapabilityID)
	assertAccountProtocolProjectionAndSingleOutbox(t, tx, created)
	clearAccountProtocolOutbox(t, tx, account.ID)
	firstID := *created.ProtocolEndpointCapabilityID
	firstKey := created.ProtocolEndpointCapability.CapabilityKey

	require.NoError(t, repo.UpdateCredentials(ctx, account.ID, map[string]any{
		"access_token": "rotated-token",
		"project_id":   "project-a",
		"plan_type":    "free",
	}))
	rotated, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, firstID, *rotated.ProtocolEndpointCapabilityID)
	require.Equal(t, firstKey, rotated.ProtocolEndpointCapability.CapabilityKey)
	assertAccountProtocolProjectionAndSingleOutbox(t, tx, rotated)
	clearAccountProtocolOutbox(t, tx, account.ID)

	require.NoError(t, repo.UpdateCredentials(ctx, account.ID, map[string]any{
		"access_token": "rotated-token",
		"project_id":   "project-a",
		"plan_type":    "pro",
	}))
	relinked, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotEqual(t, firstID, *relinked.ProtocolEndpointCapabilityID)
	require.NotEqual(t, firstKey, relinked.ProtocolEndpointCapability.CapabilityKey)
	assertAccountProtocolProjectionAndSingleOutbox(t, tx, relinked)
}

type protocolCapabilityTestQueryExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func assertAccountProtocolProjectionAndSingleOutbox(
	t *testing.T,
	db protocolCapabilityTestQueryExecutor,
	account *service.Account,
) {
	t.Helper()
	require.NotNil(t, account)
	require.NotNil(t, account.ProtocolEndpointCapability)
	assertAccountLegacyProjectionAndSingleOutbox(t, db, account.ID, account.ProtocolEndpointCapability.SupportedProtocols)
}

func assertAccountLegacyProjectionAndSingleOutbox(
	t *testing.T,
	db protocolCapabilityTestQueryExecutor,
	accountID int64,
	protocols []protocolrouter.Protocol,
) {
	t.Helper()
	update, err := service.BuildSupportedProtocolsUpdate(protocols)
	require.NoError(t, err)
	expected, err := json.Marshal(update[service.SupportedProtocolsExtraKey])
	require.NoError(t, err)
	var (
		projection  []byte
		outboxCount int
	)
	require.NoError(t, scanSingleRow(context.Background(), db, `
SELECT extra->'supported_protocols',
       (SELECT COUNT(*) FROM scheduler_outbox WHERE account_id=$1)
FROM accounts
WHERE id=$1`, []any{accountID}, &projection, &outboxCount))
	require.JSONEq(t, string(expected), string(projection))
	require.Equal(t, 1, outboxCount, "account lifecycle must publish exactly one scheduler invalidation")
}

func clearAccountProtocolOutbox(t *testing.T, db protocolCapabilityTestQueryExecutor, accountID int64) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id=$1", accountID)
	require.NoError(t, err)
}

func insertProtocolCapabilityTestAccount(t *testing.T, tx interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name, apiKey string) int64 {
	t.Helper()
	var id int64
	err := tx.QueryRowContext(context.Background(), `
INSERT INTO accounts (name, platform, type, credentials, extra, concurrency, priority, rate_multiplier, status, schedulable, auto_pause_on_expired, channel_type, quota_dimension, created_at, updated_at)
VALUES ($1, 'openai', 'api_key', jsonb_build_object('api_key',$2::text,'base_url','https://relay.example.test/v1'), '{}'::jsonb, 1, 1, 1, 'active', TRUE, FALSE, 0, 'global', NOW(), NOW())
RETURNING id`, name, apiKey).Scan(&id)
	require.NoError(t, err)
	return id
}

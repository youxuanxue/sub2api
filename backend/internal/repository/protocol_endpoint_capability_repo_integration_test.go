//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProtocolEndpointCapabilityRepositorySharesOneRowAndProjectsEveryLinkedAccount(t *testing.T) {
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
		var projection []byte
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT protocol_endpoint_capability_id, extra->'supported_protocols' FROM accounts WHERE id=$1`, accountID).Scan(&capabilityID, &projection))
		require.Equal(t, firstCapability.ID, capabilityID)
		require.JSONEq(t, `["messages","responses"]`, string(projection))
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
	firstCapabilityID := *created.ProtocolEndpointCapabilityID
	firstCapabilityKey := created.ProtocolEndpointCapability.CapabilityKey

	created.Credentials["api_key"] = "rotated-secret"
	require.NoError(t, repo.Update(ctx, created))
	rotated, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, firstCapabilityID, *rotated.ProtocolEndpointCapabilityID)
	require.Equal(t, firstCapabilityKey, rotated.ProtocolEndpointCapability.CapabilityKey)

	rotated.Credentials["base_url"] = "https://other.example.test/v1"
	require.NoError(t, repo.Update(ctx, rotated))
	relinked, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotEqual(t, firstCapabilityID, *relinked.ProtocolEndpointCapabilityID)
	require.NotEqual(t, firstCapabilityKey, relinked.ProtocolEndpointCapability.CapabilityKey)

	relinked.Type = service.AccountTypeServiceAccount
	require.NoError(t, repo.Update(ctx, relinked))
	ungoverned, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Nil(t, ungoverned.ProtocolEndpointCapabilityID)
	require.Nil(t, ungoverned.ProtocolEndpointCapability)
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

	require.NoError(t, repo.UpdateCredentials(ctx, account.ID, map[string]any{
		"access_token": "rotated-token",
		"project_id":   "project-a",
		"plan_type":    "pro",
	}))
	relinked, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotEqual(t, firstID, *relinked.ProtocolEndpointCapabilityID)
	require.NotEqual(t, firstKey, relinked.ProtocolEndpointCapability.CapabilityKey)
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

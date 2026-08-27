package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestPublishProtocolRoutingProjectionsCommitsAllLegacyVisibleEffectsTogether(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := newProtocolEndpointCapabilityRepositoryWithDB(db)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT c\.id, c\.supported_protocols`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "supported_protocols"}).
			AddRow(int64(9), `["responses"]`).
			AddRow(int64(10), `["messages"]`))
	for _, item := range []struct {
		capabilityID int64
		protocols    string
		accountID    int64
	}{
		{capabilityID: 9, protocols: `["responses"]`, accountID: 41},
		{capabilityID: 10, protocols: `["messages"]`, accountID: 42},
	} {
		mock.ExpectQuery(`UPDATE accounts\s+SET extra=jsonb_set`).
			WithArgs(item.capabilityID, item.protocols).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(item.accountID))
		accountID := item.accountID
		mock.ExpectExec(`INSERT INTO scheduler_outbox`).
			WithArgs(service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}
	mock.ExpectCommit()

	affected, err := repo.PublishProtocolRoutingProjections(context.Background())
	if err != nil {
		t.Fatalf("PublishProtocolRoutingProjections: %v", err)
	}
	if affected != 2 {
		t.Fatalf("affected = %d, want 2", affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishProtocolRoutingProjectionsRollsBackWhenOutboxFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := newProtocolEndpointCapabilityRepositoryWithDB(db)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT c\.id, c\.supported_protocols`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "supported_protocols"}).AddRow(int64(9), `["responses"]`))
	mock.ExpectQuery(`UPDATE accounts\s+SET extra=jsonb_set`).
		WithArgs(int64(9), `["responses"]`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(41)))
	accountID := int64(41)
	mock.ExpectExec(`INSERT INTO scheduler_outbox`).
		WithArgs(service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil, sqlmock.AnyArg()).
		WillReturnError(errors.New("outbox failed"))
	mock.ExpectRollback()

	if _, err := repo.PublishProtocolRoutingProjections(context.Background()); err == nil {
		t.Fatal("PublishProtocolRoutingProjections succeeded despite outbox failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCommitPreparedProbeResultPersistsCapabilityWithoutPublishingLegacyState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := newProtocolEndpointCapabilityRepositoryWithDB(db)

	identity := service.ProtocolEndpointIdentity{
		KeySchemaVersion:       1,
		Platform:               service.PlatformOpenAI,
		EndpointProfile:        "custom_api_key",
		ChannelType:            "0",
		UpstreamRequestProfile: "openai_json_v1",
		ProtocolEndpoints: map[protocolrouter.Protocol]service.ProtocolEndpoint{
			protocolrouter.ProtocolResponses: {URL: "https://relay.example.test/v1/responses"},
		},
		RoutingHeaders: map[string]string{},
	}
	identityJSON, err := identity.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	now := time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC)
	lease := service.ProtocolProbeLease{CapabilityKey: identity.Key(), Generation: 2, Revision: 1, Owner: "startup"}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)FROM protocol_endpoint_capabilities.*FOR UPDATE`).
		WithArgs(identity.Key()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "capability_key", "identity", "supported_protocols", "probe_evidence", "revision",
			"last_probed_at", "probe_lease_owner", "probe_lease_until", "probe_generation",
			"identity_conflict", "created_at", "updated_at",
		}).AddRow(int64(9), identity.Key(), string(identityJSON), `[]`, `{}`, int64(1), nil, lease.Owner, now.Add(time.Minute), lease.Generation, false, now, now))
	mock.ExpectExec(`UPDATE protocol_endpoint_capabilities`).
		WithArgs(int64(9), `["responses"]`, sqlmock.AnyArg(), int64(2), now, false, lease.Revision, lease.Generation, lease.Owner).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM accounts`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectCommit()

	updated, affected, err := repo.CommitPreparedProbeResult(context.Background(), lease, service.ProtocolCapabilityMutation{
		SupportedProtocols:    []protocolrouter.Protocol{protocolrouter.ProtocolResponses},
		InitialProbeCompleted: true,
		LastProbedAt:          now,
	})
	if err != nil {
		t.Fatalf("CommitPreparedProbeResult: %v", err)
	}
	if affected != 2 || updated.Revision != 2 {
		t.Fatalf("updated=%+v affected=%d", updated, affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

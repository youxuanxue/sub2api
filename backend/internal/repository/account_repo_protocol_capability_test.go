package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func expectUngovernedProtocolCapabilityLifecycleLoad(mock sqlmock.Sqlmock, accountID int64) {
	mock.ExpectQuery(`(?s)SELECT id, platform, type, credentials, extra, channel_type, protocol_endpoint_capability_id.*FOR UPDATE`).
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "platform", "type", "credentials", "extra", "channel_type", "protocol_endpoint_capability_id",
		}).AddRow(accountID, "", "", `{}`, `{}`, 0, nil))
}

func TestEnsureAccountProtocolEndpointCapabilitySkipsUnlinkedUngovernedAccount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	account := &service.Account{
		ID:       42,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeServiceAccount,
	}
	if err := ensureAccountProtocolEndpointCapability(context.Background(), db, account); err != nil {
		t.Fatalf("ensureAccountProtocolEndpointCapability: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProtocolEndpointCapabilitiesIncludesSharedAccountCount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT a\.id,[\s\S]*linked_account_count`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"account_id", "id", "capability_key", "identity", "supported_protocols", "probe_evidence", "revision",
			"last_probed_at", "probe_lease_owner", "probe_lease_until", "probe_generation",
			"identity_conflict", "created_at", "updated_at", "linked_account_count",
		}).AddRow(
			int64(42), int64(9), "endpoint-capability-key",
			`{"key_schema_version":1,"platform":"openai","endpoint_profile":"custom_api_key","channel_type":"openai","protocol_endpoints":{"responses":{"url":"https://relay.example.test/v1/responses","api_version":""}},"upstream_request_profile":"openai_json_v1","routing_headers":{}}`,
			`["responses"]`, `{"initial_probe_completed":true}`, int64(7), now,
			nil, nil, int64(3), false, now, now, 4,
		))

	repo := &accountRepository{sql: db}
	capabilities, err := repo.loadProtocolEndpointCapabilities(context.Background(), []int64{42})
	if err != nil {
		t.Fatalf("loadProtocolEndpointCapabilities: %v", err)
	}
	capability := capabilities[42]
	if capability == nil {
		t.Fatal("capability is nil")
	}
	if capability.LinkedAccountCount != 4 {
		t.Fatalf("linked account count = %d, want 4", capability.LinkedAccountCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureProtocolEndpointCapabilityLinkInvalidatesEveryLinkedAccountWhenSharedFactsChange(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	account := &service.Account{
		ID:       42,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://relay.example.test/v1",
		},
	}
	identity, governed, err := service.BuildProtocolEndpointIdentity(account)
	if err != nil || !governed {
		t.Fatalf("BuildProtocolEndpointIdentity = governed:%v err:%v", governed, err)
	}
	identityJSON, err := identity.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	now := time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC)

	mock.ExpectExec(`INSERT INTO protocol_endpoint_capabilities`).
		WithArgs(identity.Key(), string(identityJSON)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)FROM protocol_endpoint_capabilities.*FOR UPDATE`).
		WithArgs(identity.Key()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "capability_key", "identity", "supported_protocols", "probe_evidence", "revision",
			"last_probed_at", "probe_lease_owner", "probe_lease_until", "probe_generation",
			"identity_conflict", "created_at", "updated_at",
		}).AddRow(int64(9), identity.Key(), string(identityJSON), `["responses"]`, `{}`, int64(1), nil, nil, nil, int64(0), false, now, now))
	mock.ExpectExec(`UPDATE protocol_endpoint_capabilities`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE accounts\s+SET protocol_endpoint_capability_id`).
		WithArgs(account.ID, int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE accounts\s+SET extra=jsonb_set`).
		WithArgs(int64(9), `["messages","responses"]`).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM accounts`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`SELECT id FROM accounts WHERE protocol_endpoint_capability_id=\$1`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(41)).AddRow(int64(42)))
	for _, accountID := range []int64{41, 42} {
		mock.ExpectExec(`INSERT INTO scheduler_outbox`).
			WithArgs(service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))
	}

	_, err = ensureProtocolEndpointCapabilityLink(
		context.Background(),
		db,
		account,
		identity,
		[]protocolrouter.Protocol{protocolrouter.ProtocolMessages},
		false,
	)
	if err != nil {
		t.Fatalf("ensureProtocolEndpointCapabilityLink: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

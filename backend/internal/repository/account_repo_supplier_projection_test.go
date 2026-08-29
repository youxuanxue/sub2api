package repository

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestUS048_SupplierMetadataRepositoryWritesOnlyNameAndPriority(t *testing.T) {
	var capturedSQL []string
	matcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		capturedSQL = append(capturedSQL, actualSQL)
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil, nil)

	type supplierMetadataUpdater interface {
		UpdateSupplierMetadata(ctx context.Context, accountID int64, name string, priority int) error
	}
	updater, ok := any(repo).(supplierMetadataUpdater)
	require.True(t, ok, "production account repository must expose the supplier metadata-only write")

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE accounts\s+SET\s+name = \$1,\s+priority = \$2,\s+updated_at = NOW\(\)\s+WHERE id = \$3 AND deleted_at IS NULL`).
		WithArgs("supplier/new · 档位 3", 113, int64(41)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountChanged, int64(41), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = updater.UpdateSupplierMetadata(context.Background(), 41, "supplier/new · 档位 3", 113)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	var accountUpdateSQL string
	for _, query := range capturedSQL {
		if strings.Contains(query, "UPDATE accounts") {
			accountUpdateSQL = strings.ToLower(query)
			break
		}
	}
	require.NotEmpty(t, accountUpdateSQL)
	for _, forbidden := range []string{
		"credentials", "extra", "status", "schedulable", "concurrency", "rate_multiplier",
		"proxy_id", "load_factor", "last_used_at", "expires_at", "rate_limit_reset_at",
		"overload_until", "session_window", "quota_dimension", "parent_account_id", "group",
	} {
		require.NotContains(t, accountUpdateSQL, forbidden)
	}
}

func TestUS048_SupplierConfigurationRepositoryRejectsUnverifiedNonEmptyMapping(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil, nil)

	err = repo.UpdateSupplierProjection(context.Background(), &service.Account{
		ID:          41,
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeAPIKey,
		ChannelType: 1,
		Credentials: map[string]any{
			"base_url": "https://supplier.example/v1",
			"api_key":  "secret",
			"model_mapping": map[string]any{
				"deepseek-v4-pro": "deepseek-v4-pro",
			},
		},
		Extra: map[string]any{
			service.SupplierSourceIDExtraKey:     int64(7),
			service.SupplierDiscountBandExtraKey: 3,
		},
		Status: service.StatusActive, Schedulable: true,
	}, false)

	require.ErrorIs(t, err, service.ErrSupplierProjectionProtocolNotReady)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUS048_SupplierConfigurationRepositoryWritesOnlyOwnedFields(t *testing.T) {
	var capturedSQL []string
	matcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		capturedSQL = append(capturedSQL, actualSQL)
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil, nil)

	account := &service.Account{
		ID:          41,
		Name:        "supplier/new · 档位 3",
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeAPIKey,
		ChannelType: 1,
		Credentials: map[string]any{
			"base_url": "https://supplier.example/v1",
			"api_key":  "new-secret",
			"model_mapping": map[string]string{
				"deepseek-v4-pro": "deepseek-v4-pro",
			},
		},
		Extra: map[string]any{
			service.SupplierSourceIDExtraKey:     int64(7),
			service.SupplierDiscountBandExtraKey: 3,
			"runtime_observation":                "stale-value-must-not-be-written",
		},
		Priority:    113,
		Status:      service.StatusActive,
		Schedulable: true,
		GroupIDs:    []int64{4, 9},
	}
	identity, governed, err := service.BuildProtocolEndpointIdentity(account)
	require.NoError(t, err)
	require.True(t, governed)
	identityJSON, err := identity.CanonicalJSON()
	require.NoError(t, err)
	now := time.Date(2026, time.August, 28, 0, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE accounts\s+SET\s+name = \$1,\s+platform = \$2,\s+type = \$3,\s+channel_type = \$4,\s+credentials = \$5::jsonb,\s+extra = COALESCE\(extra, '\{\}'::jsonb\) \|\| \$6::jsonb,\s+priority = \$7,\s+status = \$8,\s+schedulable = \$9,\s+updated_at = NOW\(\)\s+WHERE id = \$10 AND deleted_at IS NULL`).
		WithArgs(
			"supplier/new · 档位 3",
			service.PlatformNewAPI,
			service.AccountTypeAPIKey,
			1,
			`{"api_key":"new-secret","base_url":"https://supplier.example/v1","model_mapping":{"deepseek-v4-pro":"deepseek-v4-pro"}}`,
			`{"supplier_discount_band":3,"supplier_source_id":7}`,
			113,
			service.StatusActive,
			true,
			int64(41),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT id, platform, type, credentials, extra, channel_type, protocol_endpoint_capability_id.*FOR UPDATE`).
		WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "platform", "type", "credentials", "extra", "channel_type", "protocol_endpoint_capability_id",
		}).AddRow(
			int64(41), service.PlatformNewAPI, service.AccountTypeAPIKey,
			`{"api_key":"new-secret","base_url":"https://supplier.example/v1","model_mapping":{"deepseek-v4-pro":"deepseek-v4-pro"}}`,
			`{"supplier_discount_band":3,"supplier_source_id":7,"supported_protocols":[]}`,
			1,
			int64(8),
		))
	mock.ExpectExec(`INSERT INTO protocol_endpoint_capabilities`).
		WithArgs(identity.Key(), string(identityJSON)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)FROM protocol_endpoint_capabilities.*FOR UPDATE`).
		WithArgs(identity.Key()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "capability_key", "identity", "supported_protocols", "probe_evidence", "revision",
			"last_probed_at", "probe_lease_owner", "probe_lease_until", "probe_generation",
			"identity_conflict", "created_at", "updated_at",
		}).AddRow(int64(9), identity.Key(), string(identityJSON), `[]`, `{}`, int64(1), nil, nil, nil, int64(0), false, now, now))
	mock.ExpectExec(`UPDATE accounts\s+SET protocol_endpoint_capability_id=\$2\s+WHERE`).
		WithArgs(int64(41), int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM accounts`).
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)UPDATE protocol_endpoint_capabilities\s+SET probe_generation=probe_generation\+1.*RETURNING capability_key, probe_generation, revision, probe_lease_owner`).
		WithArgs(identity.Key(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"capability_key", "probe_generation", "revision", "probe_lease_owner",
		}).AddRow(identity.Key(), int64(1), int64(1), "supplier-chat-probe"))
	mock.ExpectQuery(`(?s)FROM protocol_endpoint_capabilities.*FOR UPDATE`).
		WithArgs(identity.Key()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "capability_key", "identity", "supported_protocols", "probe_evidence", "revision",
			"last_probed_at", "probe_lease_owner", "probe_lease_until", "probe_generation",
			"identity_conflict", "created_at", "updated_at",
		}).AddRow(
			int64(9), identity.Key(), string(identityJSON), `[]`, `{}`, int64(1), nil,
			"supplier-chat-probe", now.Add(time.Minute), int64(1), false, now, now,
		))
	mock.ExpectExec(`UPDATE protocol_endpoint_capabilities`).
		WithArgs(
			int64(9), `["chat_completions"]`, sqlmock.AnyArg(), int64(2), sqlmock.AnyArg(), false,
			int64(1), int64(1), "supplier-chat-probe",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`UPDATE accounts\s+SET extra=jsonb_set`).
		WithArgs(int64(9), `["chat_completions"]`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(41)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountChanged, int64(41), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err = repo.UpdateSupplierProjection(context.Background(), account, true)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	var accountUpdateSQL string
	for _, query := range capturedSQL {
		if strings.Contains(query, "UPDATE accounts") {
			accountUpdateSQL = strings.ToLower(query)
			break
		}
	}
	require.NotEmpty(t, accountUpdateSQL)
	for _, required := range []string{"name", "platform", "type", "channel_type", "credentials", "extra", "priority", "status", "schedulable"} {
		require.Contains(t, accountUpdateSQL, required)
	}
	for _, forbidden := range []string{
		"notes", "concurrency", "rate_multiplier",
		"proxy_id", "load_factor", "tier_id", "last_used_at", "expires_at", "rate_limit",
		"overload_until", "session_window", "quota_dimension", "parent_account_id", "error_message", "group",
		"supplier_source_model_id", "supplier_channel", "supplier_projection_mode", "supplier_credential_fingerprint",
	} {
		require.NotContains(t, accountUpdateSQL, forbidden)
	}
}

func TestUS048_SupplierProjectionReadSelectsOnlyRequiredConfigurationFields(t *testing.T) {
	var capturedSQL []string
	matcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		capturedSQL = append(capturedSQL, actualSQL)
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil, nil)

	mock.ExpectQuery(`(?s)SELECT .* FROM "accounts" WHERE .*"id" = \$1`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "platform", "type", "credentials", "extra",
			"priority", "status", "schedulable", "channel_type",
		}).AddRow(
			int64(41), "supplier/source · 档位 3", service.PlatformNewAPI, service.AccountTypeAPIKey,
			[]byte(`{"base_url":"https://supplier.example/v1","api_key":"secret","model_mapping":{"model":"model"}}`),
			[]byte(`{"supplier_source_id":7,"supplier_discount_band":3}`),
			103, service.StatusActive, true, 1,
		))

	account, err := repo.GetSupplierAccount(context.Background(), 41)
	require.NoError(t, err)
	require.Equal(t, int64(41), account.ID)
	require.Nil(t, account.RateMultiplier)
	require.Zero(t, account.Concurrency)
	require.Nil(t, account.ProxyID)
	require.Nil(t, account.GroupIDs)
	require.NoError(t, mock.ExpectationsWereMet())

	require.Len(t, capturedSQL, 1)
	query := strings.ToLower(capturedSQL[0])
	for _, required := range []string{
		"id", "name", "platform", "type", "credentials", "extra",
		"priority", "status", "schedulable", "channel_type",
	} {
		require.Contains(t, query, required)
	}
	for _, forbidden := range []string{
		"account_groups", "rate_multiplier", "concurrency", "proxy_id", "proxy_fallback_origin_id",
		"load_factor", "notes", "error_message", "last_used_at", "expires_at", "rate_limited_at",
		"rate_limit_reset_at", "overload_until", "temp_unschedulable", "session_window", "tier_id",
		"parent_account_id", "quota_dimension",
	} {
		require.NotContains(t, query, forbidden)
	}
}

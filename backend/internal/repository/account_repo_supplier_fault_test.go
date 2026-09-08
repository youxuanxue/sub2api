package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSupplierCredentialFaultReadIncludesErrorWithoutLoadingGroups(t *testing.T) {
	var query string
	matcher := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		query = actual
		return sqlmock.QueryMatcherRegexp.Match(expected, actual)
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil, nil)
	mock.ExpectQuery(`SELECT .* FROM "accounts"`).
		WithArgs(service.PlatformNewAPI).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "platform", "type", "channel_type", "credentials", "extra", "status", "error_message", "schedulable"}).
			AddRow(int64(124), "supplier", service.PlatformNewAPI, service.AccountTypeAPIKey, 14,
				`{"base_url":"https://supplier.example","api_key":"key"}`, `{"supplier_source_id":11}`,
				service.StatusError, "Supplier credential failure: invalid key", false))
	accounts, err := repo.ListSupplierCredentialFaultAccounts(context.Background())
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, "Supplier credential failure: invalid key", accounts[0].ErrorMessage)
	require.Equal(t, "key", accounts[0].GetCredential("api_key"))
	require.False(t, accounts[0].Schedulable)
	require.NotContains(t, strings.ToLower(query), "group")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSupplierCredentialFaultUpdateGuardsSnapshotAndPreservesIndependentState(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "block", true: "recover"}[recovery], func(t *testing.T) {
			var query string
			matcher := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
				query = actual
				return sqlmock.QueryMatcherRegexp.Match(expected, actual)
			})
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			repo := newAccountRepositoryWithSQL(nil, db, nil, nil)
			account := &service.Account{
				ID: 124, Platform: service.PlatformNewAPI, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: false,
				Credentials: map[string]any{"api_key": "key", "base_url": "https://supplier.example"},
				Extra:       map[string]any{service.SupplierSourceIDExtraKey: int64(11)},
			}
			message, status := "Supplier credential failure: Invalid API key", service.StatusError
			if recovery {
				account.Status, account.ErrorMessage = service.StatusError, message
				message, status = "", service.StatusActive
			}
			credentials, err := json.Marshal(account.Credentials)
			require.NoError(t, err)
			mock.ExpectExec(`(?s)WITH updated AS .*INSERT INTO scheduler_outbox`).
				WithArgs(status, message, account.ID, service.PlatformNewAPI, service.AccountTypeAPIKey,
					string(credentials), "11", account.Status, account.ErrorMessage, service.SchedulerOutboxEventAccountChanged).
				WillReturnResult(sqlmock.NewResult(0, 1))

			changed, err := repo.UpdateSupplierCredentialFault(context.Background(), account, message)
			require.NoError(t, err)
			require.True(t, changed)
			require.NoError(t, mock.ExpectationsWereMet())
			require.Contains(t, query, "a.credentials = $6::jsonb")
			require.Contains(t, query, "a.extra->'supplier_source_id' = $7::jsonb")
			require.Contains(t, query, "a.status = $8 AND COALESCE(a.error_message, '') = $9")
			assignments := strings.Split(strings.Split(query, "SET ")[1], "WHERE")[0]
			for _, untouched := range []string{"schedulable", "concurrency", "extra", "credentials", "rate_limit", "temp_unschedulable", "overload"} {
				require.NotContains(t, assignments, untouched)
			}
		})
	}
}

func TestSupplierCredentialFaultRejectsUnrelatedErrorsAndPropagatesStoreFailures(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := newAccountRepositoryWithSQL(nil, db, nil, nil)
	account := &service.Account{
		ID: 124, Status: service.StatusError, ErrorMessage: "Protocol misconfiguration",
		Credentials: map[string]any{"api_key": "key"},
		Extra:       map[string]any{service.SupplierSourceIDExtraKey: int64(11)},
	}
	changed, err := repo.UpdateSupplierCredentialFault(context.Background(), account, "")
	require.NoError(t, err)
	require.False(t, changed)
	changed, err = repo.UpdateSupplierCredentialFault(context.Background(), account, "Supplier credential failure: invalid key")
	require.NoError(t, err)
	require.False(t, changed)

	account.Status, account.ErrorMessage = service.StatusActive, ""
	mock.ExpectExec(`(?s)WITH updated AS .*INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(0, 0))
	changed, err = repo.UpdateSupplierCredentialFault(context.Background(), account, "Supplier credential failure: invalid key")
	require.NoError(t, err)
	require.False(t, changed, "credential rotation or a concurrent state change must reject the stale write")
	mock.ExpectExec(`(?s)WITH updated AS .*INSERT INTO scheduler_outbox`).WillReturnError(errors.New("outbox unavailable"))
	changed, err = repo.UpdateSupplierCredentialFault(context.Background(), account, "Supplier credential failure: invalid key")
	require.ErrorContains(t, err, "outbox unavailable")
	require.False(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

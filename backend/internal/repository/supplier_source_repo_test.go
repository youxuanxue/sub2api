package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type supplierModelsJSONArg struct {
	want []service.SupplierSourceModel
}

func (a supplierModelsJSONArg) Match(value driver.Value) bool {
	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return false
	}
	var got []service.SupplierSourceModel
	return json.Unmarshal(raw, &got) == nil && len(got) == len(a.want) &&
		got[0].ClientModelID == a.want[0].ClientModelID &&
		got[0].UpstreamModelID == a.want[0].UpstreamModelID &&
		got[0].PurchaseRatio != nil && a.want[0].PurchaseRatio != nil &&
		*got[0].PurchaseRatio == *a.want[0].PurchaseRatio
}

func TestSupplierSourceRepositoryCreatePersistsSingleRowContract(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC)
	ratio := 0.5
	models := []service.SupplierSourceModel{{
		ClientModelID: "deepseek-v4-pro", UpstreamModelID: "deepseek-v4-pro", PurchaseRatio: &ratio,
	}}
	source := &service.SupplierSource{
		SupplierName: "佳杰", ChannelName: "stbl-5", Endpoint: "https://token.vstecscloud.com/v1",
		EncryptedCredential: "ciphertext", CredentialFingerprint: "hmac:fingerprint",
		BasePriority: 100, AccountConcurrency: 1000, Models: models, Notes: "lowest ratio only",
	}

	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO model_supplier_sources
(supplier_name, channel_name, endpoint, encrypted_credential, credential_fingerprint, base_priority, account_concurrency, models, notes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, created_at, updated_at`)).
		WithArgs("佳杰", "stbl-5", "https://token.vstecscloud.com/v1", "ciphertext", "hmac:fingerprint", 100, 1000, supplierModelsJSONArg{want: models}, "lowest ratio only").
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(7), now, now))

	repo := NewSupplierSourceRepository(db)
	require.NoError(t, repo.Create(context.Background(), source))
	require.Equal(t, int64(7), source.ID)
	require.Equal(t, now, source.CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSupplierSourceRepositoryUpdateReplacesSingleRowContract(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	ratio := 0.8
	models := []service.SupplierSourceModel{{
		ClientModelID: "qwen-3.7-max", UpstreamModelID: "qwen-3.7-max", PurchaseRatio: &ratio,
	}}
	source := &service.SupplierSource{
		ID: 7, SupplierName: "VSTECS", ChannelName: "stbl-5", Endpoint: "https://token.vstecscloud.com/v1",
		EncryptedCredential: "rotated-ciphertext", CredentialFingerprint: "hmac:rotated",
		BasePriority: 120, AccountConcurrency: 1000, Models: models, Notes: "rotated",
	}

	mock.ExpectQuery(regexp.QuoteMeta(`UPDATE model_supplier_sources
SET supplier_name=$1, channel_name=$2, endpoint=$3, encrypted_credential=$4,
    credential_fingerprint=$5, base_priority=$6, account_concurrency=$7, models=$8, notes=$9, updated_at=NOW()
WHERE id=$10
RETURNING updated_at`)).
		WithArgs("VSTECS", "stbl-5", "https://token.vstecscloud.com/v1", "rotated-ciphertext", "hmac:rotated", 120, 1000, supplierModelsJSONArg{want: models}, "rotated", int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"updated_at"}).AddRow(now))

	repo := NewSupplierSourceRepository(db)
	require.NoError(t, repo.Update(context.Background(), source))
	require.Equal(t, now, source.UpdatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSupplierSourceRepositoryGetReturnsModelsFromJSON(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	modelsJSON := []byte(`[{"client_model_id":"deepseek-v4-pro","upstream_model_id":"deepseek-v4-pro","purchase_ratio":0.5}]`)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, supplier_name, channel_name, endpoint, encrypted_credential,
credential_fingerprint, base_priority, account_concurrency, models, notes, created_at, updated_at
FROM model_supplier_sources WHERE id=$1`)).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "supplier_name", "channel_name", "endpoint", "encrypted_credential",
			"credential_fingerprint", "base_priority", "account_concurrency", "models", "notes", "created_at", "updated_at",
		}).AddRow(int64(7), "佳杰", "stbl-5", "https://token.vstecscloud.com/v1", "ciphertext", "hmac:fingerprint", 100, 1000, modelsJSON, "", now, now))

	repo := NewSupplierSourceRepository(db)
	source, err := repo.Get(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "ciphertext", source.EncryptedCredential)
	require.Equal(t, 100, source.BasePriority)
	require.Len(t, source.Models, 1)
	require.Equal(t, "deepseek-v4-pro", source.Models[0].ClientModelID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSupplierSourceRepositoryCreateClassifiesIdentityConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO model_supplier_sources`)).
		WillReturnError(&pq.Error{Code: "23505", Constraint: "model_supplier_sources_identity_unique"})

	repo := NewSupplierSourceRepository(db)
	err = repo.Create(context.Background(), &service.SupplierSource{
		SupplierName: "佳杰", ChannelName: "stbl-5", Endpoint: "https://token.vstecscloud.com/v1",
		EncryptedCredential: "ciphertext", CredentialFingerprint: "hmac:fingerprint", BasePriority: 100,
	})
	require.True(t, errors.Is(err, service.ErrSupplierSourceIdentityConflict))
	require.NoError(t, mock.ExpectationsWereMet())
}

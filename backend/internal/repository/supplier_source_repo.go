package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type supplierSourceRepository struct {
	db *sql.DB
}

func NewSupplierSourceRepository(db *sql.DB) service.SupplierSourceRepository {
	return &supplierSourceRepository{db: db}
}

func (r *supplierSourceRepository) Create(ctx context.Context, source *service.SupplierSource) error {
	if source == nil {
		return service.ErrSupplierSourceInvalidInput
	}
	models, err := marshalSupplierSourceModels(source.Models)
	if err != nil {
		return err
	}
	err = r.db.QueryRowContext(ctx, `INSERT INTO model_supplier_sources
(supplier_name, channel_name, channel_type, endpoint, encrypted_credential, credential_fingerprint, base_priority, models, notes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, created_at, updated_at`,
		source.SupplierName,
		source.ChannelName,
		source.ChannelType,
		source.Endpoint,
		source.EncryptedCredential,
		source.CredentialFingerprint,
		source.BasePriority,
		models,
		source.Notes,
	).Scan(&source.ID, &source.CreatedAt, &source.UpdatedAt)
	if err != nil {
		return translateSupplierSourceWriteError("insert supplier source", err)
	}
	return nil
}

func (r *supplierSourceRepository) Update(ctx context.Context, source *service.SupplierSource) error {
	if source == nil || source.ID <= 0 {
		return service.ErrSupplierSourceInvalidInput
	}
	models, err := marshalSupplierSourceModels(source.Models)
	if err != nil {
		return err
	}
	err = r.db.QueryRowContext(ctx, `UPDATE model_supplier_sources
SET supplier_name=$1, channel_name=$2, channel_type=$3, endpoint=$4, encrypted_credential=$5,
    credential_fingerprint=$6, base_priority=$7, models=$8, notes=$9, updated_at=NOW()
WHERE id=$10
RETURNING updated_at`,
		source.SupplierName,
		source.ChannelName,
		source.ChannelType,
		source.Endpoint,
		source.EncryptedCredential,
		source.CredentialFingerprint,
		source.BasePriority,
		models,
		source.Notes,
		source.ID,
	).Scan(&source.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrSupplierSourceNotFound
	}
	if err != nil {
		return translateSupplierSourceWriteError("update supplier source", err)
	}
	return nil
}

func (r *supplierSourceRepository) Get(ctx context.Context, id int64) (*service.SupplierSource, error) {
	return scanSupplierSource(r.db.QueryRowContext(ctx, `SELECT id, supplier_name, channel_name, channel_type, endpoint, encrypted_credential,
credential_fingerprint, base_priority, models, notes, created_at, updated_at
FROM model_supplier_sources WHERE id=$1`, id))
}

func (r *supplierSourceRepository) List(ctx context.Context) ([]service.SupplierSource, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, supplier_name, channel_name, channel_type, endpoint, encrypted_credential,
credential_fingerprint, base_priority, models, notes, created_at, updated_at
FROM model_supplier_sources ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list supplier sources: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sources := make([]service.SupplierSource, 0)
	for rows.Next() {
		source, scanErr := scanSupplierSource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		sources = append(sources, *source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list supplier sources: %w", err)
	}
	return sources, nil
}

type supplierSourceScanner interface {
	Scan(dest ...any) error
}

func scanSupplierSource(scanner supplierSourceScanner) (*service.SupplierSource, error) {
	source := &service.SupplierSource{}
	var models []byte
	err := scanner.Scan(
		&source.ID,
		&source.SupplierName,
		&source.ChannelName,
		&source.ChannelType,
		&source.Endpoint,
		&source.EncryptedCredential,
		&source.CredentialFingerprint,
		&source.BasePriority,
		&models,
		&source.Notes,
		&source.CreatedAt,
		&source.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSupplierSourceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan supplier source: %w", err)
	}
	if err := json.Unmarshal(models, &source.Models); err != nil {
		return nil, fmt.Errorf("decode supplier source models: %w", err)
	}
	if source.Models == nil {
		source.Models = []service.SupplierSourceModel{}
	}
	return source, nil
}

func marshalSupplierSourceModels(models []service.SupplierSourceModel) ([]byte, error) {
	if models == nil {
		models = []service.SupplierSourceModel{}
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		return nil, fmt.Errorf("encode supplier source models: %w", err)
	}
	return encoded, nil
}

func translateSupplierSourceWriteError(operation string, err error) error {
	var postgresErr *pq.Error
	if errors.As(err, &postgresErr) && postgresErr.Code == "23505" && postgresErr.Constraint == "model_supplier_sources_identity_unique" {
		return service.ErrSupplierSourceIdentityConflict
	}
	return fmt.Errorf("%s: %w", operation, err)
}

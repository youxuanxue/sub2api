package repository

import (
	"context"
	"encoding/json"
	"fmt"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *accountRepository) ListSupplierCredentialFaultAccounts(ctx context.Context) ([]service.Account, error) {
	accounts, err := r.client.Account.Query().Where(
		dbaccount.DeletedAtIsNil(), dbaccount.PlatformEQ(service.PlatformNewAPI),
	).Select(
		dbaccount.FieldID, dbaccount.FieldName, dbaccount.FieldPlatform, dbaccount.FieldType,
		dbaccount.FieldChannelType, dbaccount.FieldCredentials, dbaccount.FieldExtra,
		dbaccount.FieldStatus, dbaccount.FieldErrorMessage, dbaccount.FieldSchedulable,
	).All(ctx)
	if err != nil {
		return nil, err
	}
	return accountsToSupplierSourceService(accounts), nil
}

func (r *accountRepository) UpdateSupplierCredentialFault(ctx context.Context, snapshot *service.Account, message string) (bool, error) {
	if snapshot == nil || !service.IsSupplierManagedAccount(snapshot) {
		return false, nil
	}
	status := service.StatusError
	if message == "" {
		if snapshot.Status != service.StatusError || !service.IsSupplierCredentialFault(snapshot.ErrorMessage) {
			return false, nil
		}
		status = service.StatusActive
	} else if !service.IsSupplierCredentialFault(message) ||
		(snapshot.Status != service.StatusActive && (snapshot.Status != service.StatusError || !service.IsSupplierCredentialFault(snapshot.ErrorMessage))) {
		return false, nil
	}
	credentials, err := json.Marshal(snapshot.Credentials)
	if err != nil {
		return false, fmt.Errorf("marshal supplier credential snapshot: %w", err)
	}
	sourceID, err := json.Marshal(snapshot.Extra[service.SupplierSourceIDExtraKey])
	if err != nil {
		return false, fmt.Errorf("marshal supplier source identity: %w", err)
	}
	// Keep schedulable unchanged: status owns this fault, while an operator's
	// independent pause and every model/protocol limit retain their own state.
	result, err := r.sql.ExecContext(ctx, `
		WITH updated AS (
			UPDATE accounts AS a
			SET status = $1, error_message = $2, updated_at = NOW()
			WHERE a.id = $3 AND a.deleted_at IS NULL
				AND a.platform = $4 AND a.type = $5
				AND a.credentials = $6::jsonb
				AND a.extra->'supplier_source_id' = $7::jsonb
				AND a.status = $8 AND COALESCE(a.error_message, '') = $9
			RETURNING a.id
		)
		INSERT INTO scheduler_outbox (event_type, account_id, created_at)
		SELECT $10, id, NOW() FROM updated`,
		status, message, snapshot.ID, service.PlatformNewAPI, service.AccountTypeAPIKey,
		string(credentials), string(sourceID), snapshot.Status, snapshot.ErrorMessage,
		service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, snapshot.ID)
	return true, nil
}

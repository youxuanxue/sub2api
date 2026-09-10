package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const cursorOAuthAccountSQL = `(platform = 'newapi' AND type = 'apikey' AND channel_type = 14 AND extra->>'upstream_provider' = 'cursor')`

func (r *accountRepository) SetCursorOAuthRefreshFailureIfUnchanged(ctx context.Context, id int64, expected map[string]any, proxyID *int64, reason string, until *time.Time) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("account repository SQL executor is not configured")
	}
	oldJSON, err := json.Marshal(normalizeJSONMap(expected))
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, `
		WITH updated AS (
			UPDATE accounts SET
				status = CASE WHEN $1::timestamptz IS NULL THEN 'error' ELSE status END,
				error_message = CASE WHEN $1::timestamptz IS NULL THEN $2 ELSE error_message END,
				temp_unschedulable_until = CASE WHEN $1::timestamptz IS NOT NULL THEN $1 ELSE temp_unschedulable_until END,
				temp_unschedulable_reason = CASE WHEN $1::timestamptz IS NOT NULL THEN $2 ELSE temp_unschedulable_reason END,
				updated_at = NOW()
			WHERE id = $3 AND deleted_at IS NULL AND `+cursorOAuthAccountSQL+`
				AND credentials = $4::jsonb AND proxy_id IS NOT DISTINCT FROM $5
				AND status = 'active' AND schedulable = TRUE
			RETURNING id
		)
		INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
		SELECT $6, id, NULL, NULL FROM updated`, until, reason, id, string(oldJSON), proxyID, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return true, nil
}

// Credential rotation, the scheduling expiry and cache invalidation commit
// together. Concurrent reauthorization wins; operator pause fields are untouched.
func (r *accountRepository) UpdateCursorOAuthCredentialsIfUnchanged(ctx context.Context, id int64, expected map[string]any, proxyID *int64, credentials map[string]any, expires time.Time) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("account repository SQL executor is not configured")
	}
	oldJSON, err := json.Marshal(normalizeJSONMap(expected))
	if err != nil {
		return false, err
	}
	newJSON, err := json.Marshal(normalizeJSONMap(credentials))
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, `
		WITH updated AS (
			UPDATE accounts SET credentials = $1::jsonb, expires_at = $2, updated_at = NOW()
			WHERE id = $3 AND deleted_at IS NULL AND `+cursorOAuthAccountSQL+`
				AND credentials = $4::jsonb AND proxy_id IS NOT DISTINCT FROM $5
			RETURNING id
		)
		INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
		SELECT $6, id, NULL, NULL FROM updated`, string(newJSON), expires, id, string(oldJSON), proxyID, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return true, nil
}

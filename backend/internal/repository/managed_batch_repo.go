package repository

// This file owns the durable state used by the managed Batch executor.  It is
// deliberately independent of HTTP handlers and provider adapters: callers
// pass the normalized batchcontract values and this repository only performs
// owner-scoped reads and transactional claims.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type ManagedBatchFile struct {
	ID, Purpose, Filename, StorageRef, Status string
	UserID, APIKeyID                          int64
	ByteSize                                  int64
	SHA256                                    *string
	ExpiresAt                                 *time.Time
}

type ManagedBatch struct {
	ID, InputFileID, Endpoint, CompletionWindow, Status string
	UserID, APIKeyID                                    int64
	RequestCounts, Metadata                             map[string]any
	OutputFileID, ErrorFileID                           *string
	LastErrorCode, LastErrorMessage                     *string
	DeadlineAt, RetryAt                                 *time.Time
	Version                                             int64
}

type ManagedBatchItem struct {
	ID                                     int64
	BatchID, CustomID, Status, LeaseOwner  string
	Request, Response, Error               map[string]any
	AttemptCount                           int
	NextAttemptAt, LeaseExpiresAt          *time.Time
	InputTokens, OutputTokens, TotalTokens int64
	UnknownResult                          bool
}

type ManagedBatchOutboxEvent struct {
	ID                                   int64
	BatchID, EventType, DedupKey, Status string
	Payload                              map[string]any
	Attempts                             int
	AvailableAt, LockedUntil             *time.Time
}

type ManagedBatchRepository struct{ db *sql.DB }

func NewManagedBatchRepository(db *sql.DB) *ManagedBatchRepository {
	return &ManagedBatchRepository{db: db}
}

func (r *ManagedBatchRepository) CreateFile(ctx context.Context, f ManagedBatchFile) error {
	if r == nil || r.db == nil {
		return errors.New("managed batch repository: nil db")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO batch_files
		(id,user_id,api_key_id,purpose,filename,storage_ref,byte_size,sha256,status,expires_at)
		VALUES ($1,$2,NULLIF($3,0),$4,$5,$6,$7,$8,$9,$10)`, f.ID, f.UserID, f.APIKeyID, f.Purpose, f.Filename, f.StorageRef, f.ByteSize, f.SHA256, f.Status, f.ExpiresAt)
	return err
}

func (r *ManagedBatchRepository) CreateBatch(ctx context.Context, b ManagedBatch) error {
	if r == nil || r.db == nil {
		return errors.New("managed batch repository: nil db")
	}
	rc, err := json.Marshal(b.RequestCounts)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(b.Metadata)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO batches
		(id,user_id,api_key_id,input_file_id,endpoint,completion_window,status,request_counts,output_file_id,error_file_id,metadata,last_error_code,last_error_message,deadline_at,retry_at,version)
		VALUES ($1,$2,NULLIF($3,0),$4,$5,$6,$7,$8::jsonb,NULLIF($9,''),NULLIF($10,''),$11::jsonb,$12,$13,$14,$15,$16)`,
		b.ID, b.UserID, b.APIKeyID, b.InputFileID, b.Endpoint, b.CompletionWindow, b.Status, rc, managedBatchValueOrEmpty(b.OutputFileID), managedBatchValueOrEmpty(b.ErrorFileID), meta, b.LastErrorCode, b.LastErrorMessage, b.DeadlineAt, b.RetryAt, b.Version)
	return err
}

func (r *ManagedBatchRepository) GetBatchForOwner(ctx context.Context, userID, apiKeyID int64, id string) (*ManagedBatch, error) {
	var b ManagedBatch
	var counts, meta []byte
	var out, ef, code, msg sql.NullString
	var retry sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT id,user_id,COALESCE(api_key_id,0),input_file_id,endpoint,completion_window,status,request_counts,output_file_id,error_file_id,metadata,last_error_code,last_error_message,deadline_at,retry_at,version FROM batches WHERE id=$1 AND user_id=$2 AND (api_key_id=$3 OR api_key_id IS NULL)`, id, userID, apiKeyID).Scan(&b.ID, &b.UserID, &b.APIKeyID, &b.InputFileID, &b.Endpoint, &b.CompletionWindow, &b.Status, &counts, &out, &ef, &meta, &code, &msg, &b.DeadlineAt, &retry, &b.Version)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(counts, &b.RequestCounts); err != nil {
		return nil, fmt.Errorf("request_counts: %w", err)
	}
	if err := json.Unmarshal(meta, &b.Metadata); err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	if out.Valid {
		b.OutputFileID = &out.String
	}
	if ef.Valid {
		b.ErrorFileID = &ef.String
	}
	if code.Valid {
		b.LastErrorCode = &code.String
	}
	if msg.Valid {
		b.LastErrorMessage = &msg.String
	}
	if retry.Valid {
		b.RetryAt = &retry.Time
	}
	return &b, nil
}

// CompareAndSwapBatchStatus updates a batch only if both owner and version
// still match. The version is incremented atomically and the bool is false for
// a stale worker or an owner mismatch.
func (r *ManagedBatchRepository) CompareAndSwapBatchStatus(ctx context.Context, userID, apiKeyID int64, id string, version int64, status string, retryAt *time.Time, errCode, errMessage *string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE batches SET status=$1,retry_at=$2,last_error_code=$3,last_error_message=$4,version=version+1,updated_at=NOW() WHERE id=$5 AND user_id=$6 AND (api_key_id=$7 OR api_key_id IS NULL) AND version=$8`, status, retryAt, errCode, errMessage, id, userID, apiKeyID, version)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClaimItems uses PostgreSQL row locks and SKIP LOCKED, so multiple executors
// can drain the same batch without duplicate leases.
func (r *ManagedBatchRepository) ClaimItems(ctx context.Context, batchID, worker string, limit int, leaseTTL time.Duration) ([]ManagedBatchItem, error) {
	if limit <= 0 {
		limit = 100
	}
	if leaseTTL <= 0 {
		leaseTTL = 5 * time.Minute
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `WITH picked AS (
		SELECT id FROM batch_items WHERE batch_id=$1 AND status IN ('pending','retry') AND (next_attempt_at IS NULL OR next_attempt_at<=NOW()) AND (lease_expires_at IS NULL OR lease_expires_at<NOW()) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $3
	) UPDATE batch_items i SET status='running',lease_owner=$2,lease_expires_at=NOW()+($4 * INTERVAL '1 second'),attempt_count=attempt_count+1,updated_at=NOW() FROM picked WHERE i.id=picked.id RETURNING i.id,i.batch_id,i.custom_id,i.status,i.request,i.attempt_count,i.lease_owner,i.lease_expires_at`, batchID, worker, limit, int64(leaseTTL/time.Second))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]ManagedBatchItem, 0, limit)
	for rows.Next() {
		var it ManagedBatchItem
		var req []byte
		if err := rows.Scan(&it.ID, &it.BatchID, &it.CustomID, &it.Status, &req, &it.AttemptCount, &it.LeaseOwner, &it.LeaseExpiresAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(req, &it.Request); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *ManagedBatchRepository) CompleteItem(ctx context.Context, id int64, worker, status string, response, itemErr map[string]any, inputTokens, outputTokens int64, unknown bool) (bool, error) {
	resp, _ := json.Marshal(response)
	eraw, _ := json.Marshal(itemErr)
	res, err := r.db.ExecContext(ctx, `UPDATE batch_items SET status=$1,response=$2::jsonb,error=$3::jsonb,input_tokens=$4,output_tokens=$5,total_tokens=$4+$5,unknown_result=$6,lease_owner=NULL,lease_expires_at=NULL,completed_at=CASE WHEN $1 IN ('succeeded','failed','cancelled','expired') THEN NOW() ELSE completed_at END,updated_at=NOW() WHERE id=$7 AND lease_owner=$8`, status, resp, eraw, inputTokens, outputTokens, unknown, id, worker)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *ManagedBatchRepository) AppendEvent(ctx context.Context, batchID string, itemID, shardID *int64, eventType string, payload map[string]any, eventKey *string) error {
	b, _ := json.Marshal(payload)
	_, err := r.db.ExecContext(ctx, `INSERT INTO batch_events (batch_id,item_id,shard_id,event_type,payload,event_key) VALUES ($1,$2,$3,$4,$5::jsonb,$6) ON CONFLICT (batch_id,event_key) WHERE event_key IS NOT NULL DO NOTHING`, batchID, itemID, shardID, eventType, b, eventKey)
	return err
}

func (r *ManagedBatchRepository) EnqueueOutbox(ctx context.Context, e ManagedBatchOutboxEvent) error {
	b, _ := json.Marshal(e.Payload)
	_, err := r.db.ExecContext(ctx, `INSERT INTO batch_outbox (batch_id,event_type,payload,dedup_key,available_at) VALUES ($1,$2,$3::jsonb,$4,COALESCE($5,NOW())) ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`, e.BatchID, e.EventType, b, e.DedupKey, e.AvailableAt)
	return err
}

func (r *ManagedBatchRepository) ClaimOutbox(ctx context.Context, worker string, limit int, leaseTTL time.Duration) ([]ManagedBatchOutboxEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if leaseTTL <= 0 {
		leaseTTL = time.Minute
	}
	rows, err := r.db.QueryContext(ctx, `WITH picked AS (SELECT id FROM batch_outbox WHERE status='pending' AND available_at<=NOW() AND (locked_until IS NULL OR locked_until<NOW()) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $2) UPDATE batch_outbox o SET status='processing',locked_by=$1,locked_until=NOW()+($3 * INTERVAL '1 second'),attempts=attempts+1 WHERE o.id IN (SELECT id FROM picked) RETURNING o.id,o.batch_id,o.event_type,o.payload,o.dedup_key,o.attempts,o.available_at,o.locked_until`, worker, limit, int64(leaseTTL/time.Second))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]ManagedBatchOutboxEvent, 0, limit)
	for rows.Next() {
		var e ManagedBatchOutboxEvent
		var p []byte
		var key sql.NullString
		if err := rows.Scan(&e.ID, &e.BatchID, &e.EventType, &p, &key, &e.Attempts, &e.AvailableAt, &e.LockedUntil); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(p, &e.Payload); err != nil {
			return nil, err
		}
		if key.Valid {
			e.DedupKey = key.String
		}
		e.Status = "processing"
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *ManagedBatchRepository) AckOutbox(ctx context.Context, id int64, worker string, failed bool, message *string) (bool, error) {
	status := "sent"
	if failed {
		status = "pending"
	}
	res, err := r.db.ExecContext(ctx, `UPDATE batch_outbox SET status=$1,locked_by=NULL,locked_until=NULL,last_error=$2,sent_at=CASE WHEN $1='sent' THEN NOW() ELSE sent_at END,available_at=CASE WHEN $1='pending' THEN NOW() ELSE available_at END WHERE id=$3 AND locked_by=$4`, status, message, id, worker)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func managedBatchValueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

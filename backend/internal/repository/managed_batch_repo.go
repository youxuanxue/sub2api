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
	CreatedAt                                 time.Time
}

// GetFileForOwner is intentionally owner-scoped. Callers must translate every
// sql.ErrNoRows (including an owner mismatch) to their public 404 sentinel.
func (r *ManagedBatchRepository) GetFileForOwner(ctx context.Context, userID, apiKeyID int64, id string) (*ManagedBatchFile, error) {
	var f ManagedBatchFile
	var sha sql.NullString
	var exp sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT id,purpose,filename,storage_ref,byte_size,sha256,status,user_id,COALESCE(api_key_id,0),expires_at,created_at FROM batch_files WHERE id=$1 AND user_id=$2 AND api_key_id=$3`, id, userID, apiKeyID).
		Scan(&f.ID, &f.Purpose, &f.Filename, &f.StorageRef, &f.ByteSize, &sha, &f.Status, &f.UserID, &f.APIKeyID, &exp, &f.CreatedAt)
	if err != nil {
		return nil, err
	}
	if sha.Valid {
		f.SHA256 = &sha.String
	}
	if exp.Valid {
		f.ExpiresAt = &exp.Time
	}
	return &f, nil
}

func (r *ManagedBatchRepository) DeleteFileForOwner(ctx context.Context, userID, apiKeyID int64, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM batch_files WHERE id=$1 AND user_id=$2 AND api_key_id=$3`, id, userID, apiKeyID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *ManagedBatchRepository) ListFilesForOwner(ctx context.Context, userID, apiKeyID int64, purpose, after string, limit int) ([]ManagedBatchFile, bool, error) {
	if limit <= 0 {
		limit = 10000
	}
	if limit > 10000 {
		limit = 10000
	}
	where := `user_id=$1 AND api_key_id=$2`
	args := []any{userID, apiKeyID}
	if purpose != "" {
		where += ` AND purpose=$3`
		args = append(args, purpose)
	}
	if after != "" {
		cursorArg := len(args) + 1
		where += fmt.Sprintf(` AND (created_at,id) < (SELECT created_at,id FROM batch_files WHERE id=$%d AND user_id=$1 AND api_key_id=$2)`, cursorArg)
		args = append(args, after)
	}
	limitArg := len(args) + 1
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, `SELECT id,purpose,filename,storage_ref,byte_size,sha256,status,user_id,COALESCE(api_key_id,0),expires_at,created_at FROM batch_files WHERE `+where+` ORDER BY created_at DESC,id DESC LIMIT $`+fmt.Sprint(limitArg), args...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	files := make([]ManagedBatchFile, 0, limit)
	for rows.Next() {
		var f ManagedBatchFile
		var sha sql.NullString
		var exp sql.NullTime
		if err := rows.Scan(&f.ID, &f.Purpose, &f.Filename, &f.StorageRef, &f.ByteSize, &sha, &f.Status, &f.UserID, &f.APIKeyID, &exp, &f.CreatedAt); err != nil {
			return nil, false, err
		}
		if sha.Valid {
			f.SHA256 = &sha.String
		}
		if exp.Valid {
			f.ExpiresAt = &exp.Time
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(files) > limit
	if more {
		files = files[:limit]
	}
	return files, more, nil
}

type ManagedBatch struct {
	ID, InputFileID, Endpoint, CompletionWindow, Status string
	UserID, APIKeyID                                    int64
	RequestCounts, Metadata                             map[string]any
	OutputFileID, ErrorFileID                           *string
	LastErrorCode, LastErrorMessage                     *string
	DeadlineAt, RetryAt                                 *time.Time
	InProgressAt, FinalizingAt, CancellingAt            *time.Time
	CompletedAt, CancelledAt, ExpiredAt, FailedAt       *time.Time
	Version                                             int64
	CreatedAt                                           time.Time
	IdempotencyKey, RequestHash                         string
}

type ManagedBatchSubmission struct {
	Batch  ManagedBatch
	Items  []ManagedBatchItem
	Outbox ManagedBatchOutboxEvent
}

// CreateBatchSubmission is the single persistence seam used by HTTP submit.
// Batch, items and the durable outbox handoff are committed atomically.
func (r *ManagedBatchRepository) CreateBatchSubmission(ctx context.Context, s ManagedBatchSubmission) (*ManagedBatch, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rc, err := json.Marshal(s.Batch.RequestCounts)
	if err != nil {
		return nil, err
	}
	meta, err := json.Marshal(s.Batch.Metadata)
	if err != nil {
		return nil, err
	}
	var insertedID string
	err = tx.QueryRowContext(ctx, `INSERT INTO batches (id,user_id,api_key_id,input_file_id,endpoint,completion_window,status,request_counts,output_file_id,error_file_id,metadata,last_error_code,last_error_message,deadline_at,retry_at,version,idempotency_key,request_hash) VALUES ($1,$2,NULLIF($3,0),$4,$5,$6,$7,$8::jsonb,NULLIF($9,''),NULLIF($10,''),$11::jsonb,$12,$13,$14,$15,$16,NULLIF($17,''),NULLIF($18,'')) ON CONFLICT (user_id,api_key_id,idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING RETURNING id`, s.Batch.ID, s.Batch.UserID, s.Batch.APIKeyID, s.Batch.InputFileID, s.Batch.Endpoint, s.Batch.CompletionWindow, s.Batch.Status, rc, managedBatchValueOrEmpty(s.Batch.OutputFileID), managedBatchValueOrEmpty(s.Batch.ErrorFileID), meta, s.Batch.LastErrorCode, s.Batch.LastErrorMessage, s.Batch.DeadlineAt, s.Batch.RetryAt, s.Batch.Version, s.Batch.IdempotencyKey, s.Batch.RequestHash).Scan(&insertedID)
	if errors.Is(err, sql.ErrNoRows) {
		existing, lookupErr := getManagedBatchRow(ctx, tx, `WHERE user_id=$1 AND api_key_id=$2 AND idempotency_key=$3`, s.Batch.UserID, s.Batch.APIKeyID, s.Batch.IdempotencyKey)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if existing.RequestHash == s.Batch.RequestHash {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return existing, nil
		}
		return nil, ErrManagedBatchIdempotencyConflict
	}
	if err != nil {
		return nil, err
	}
	for _, item := range s.Items {
		raw, marshalErr := json.Marshal(item.Request)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO batch_items (batch_id,custom_id,request,status,attempt_count) VALUES ($1,$2,$3::jsonb,$4,$5)`, s.Batch.ID, item.CustomID, raw, item.Status, item.AttemptCount); err != nil {
			return nil, err
		}
	}
	payload, err := json.Marshal(s.Outbox.Payload)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO batch_outbox (batch_id,event_type,payload,dedup_key,available_at) VALUES ($1,$2,$3::jsonb,$4,COALESCE($5,NOW())) ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`, s.Outbox.BatchID, s.Outbox.EventType, payload, s.Outbox.DedupKey, s.Outbox.AvailableAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = insertedID
	return &s.Batch, nil
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

var ErrManagedBatchIdempotencyConflict = errors.New("managed batch idempotency conflict")

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
	return getManagedBatchRow(ctx, r.db, `WHERE id=$1 AND user_id=$2 AND api_key_id=$3`, id, userID, apiKeyID)
}

type managedBatchQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getManagedBatchRow(ctx context.Context, q managedBatchQuerier, where string, args ...any) (*ManagedBatch, error) {
	var b ManagedBatch
	var counts, meta []byte
	var out, ef, code, msg, idem, hash sql.NullString
	var retry sql.NullTime
	var inProgress, finalizing, cancelling, completed, cancelled, expired, failed sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT id,user_id,COALESCE(api_key_id,0),input_file_id,endpoint,completion_window,status,request_counts,output_file_id,error_file_id,metadata,last_error_code,last_error_message,deadline_at,retry_at,in_progress_at,finalizing_at,cancelling_at,completed_at,cancelled_at,expired_at,failed_at,version,created_at,idempotency_key,request_hash FROM batches `+where, args...).Scan(&b.ID, &b.UserID, &b.APIKeyID, &b.InputFileID, &b.Endpoint, &b.CompletionWindow, &b.Status, &counts, &out, &ef, &meta, &code, &msg, &b.DeadlineAt, &retry, &inProgress, &finalizing, &cancelling, &completed, &cancelled, &expired, &failed, &b.Version, &b.CreatedAt, &idem, &hash)
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
	for _, item := range []struct {
		value  sql.NullTime
		target **time.Time
	}{{inProgress, &b.InProgressAt}, {finalizing, &b.FinalizingAt}, {cancelling, &b.CancellingAt}, {completed, &b.CompletedAt}, {cancelled, &b.CancelledAt}, {expired, &b.ExpiredAt}, {failed, &b.FailedAt}} {
		if item.value.Valid {
			v := item.value.Time
			*item.target = &v
		}
	}
	if idem.Valid {
		b.IdempotencyKey = idem.String
	}
	if hash.Valid {
		b.RequestHash = hash.String
	}
	return &b, nil
}

func (r *ManagedBatchRepository) ListBatchesForOwner(ctx context.Context, userID, apiKeyID int64, after string, limit int) ([]ManagedBatch, bool, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	args := []any{userID, apiKeyID, limit + 1}
	where := `user_id=$1 AND api_key_id=$2`
	if after != "" {
		where += ` AND (created_at,id) < (SELECT created_at,id FROM batches WHERE id=$3 AND user_id=$1 AND api_key_id=$2)`
		args = []any{userID, apiKeyID, after, limit + 1}
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,user_id,COALESCE(api_key_id,0),input_file_id,endpoint,completion_window,status,request_counts,metadata,in_progress_at,finalizing_at,cancelling_at,completed_at,cancelled_at,expired_at,failed_at,deadline_at,version,created_at FROM batches WHERE `+where+` ORDER BY created_at DESC,id DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]ManagedBatch, 0, limit)
	for rows.Next() {
		var b ManagedBatch
		var counts, meta []byte
		var inProgress, finalizing, cancelling, completed, cancelled, expired, failed sql.NullTime
		if err := rows.Scan(&b.ID, &b.UserID, &b.APIKeyID, &b.InputFileID, &b.Endpoint, &b.CompletionWindow, &b.Status, &counts, &meta, &inProgress, &finalizing, &cancelling, &completed, &cancelled, &expired, &failed, &b.DeadlineAt, &b.Version, &b.CreatedAt); err != nil {
			return nil, false, err
		}
		for _, item := range []struct {
			value  sql.NullTime
			target **time.Time
		}{{inProgress, &b.InProgressAt}, {finalizing, &b.FinalizingAt}, {cancelling, &b.CancellingAt}, {completed, &b.CompletedAt}, {cancelled, &b.CancelledAt}, {expired, &b.ExpiredAt}, {failed, &b.FailedAt}} {
			if item.value.Valid {
				v := item.value.Time
				*item.target = &v
			}
		}
		if err := json.Unmarshal(counts, &b.RequestCounts); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal(meta, &b.Metadata); err != nil {
			return nil, false, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// CompareAndSwapBatchStatus updates a batch only if both owner and version
// still match. The version is incremented atomically and the bool is false for
// a stale worker or an owner mismatch.
func (r *ManagedBatchRepository) CompareAndSwapBatchStatus(ctx context.Context, userID, apiKeyID int64, id string, version int64, status string, retryAt *time.Time, errCode, errMessage *string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE batches SET status=$1,retry_at=$2,last_error_code=$3,last_error_message=$4,
		in_progress_at=CASE WHEN $1='in_progress' AND in_progress_at IS NULL THEN NOW() ELSE in_progress_at END,
		finalizing_at=CASE WHEN $1='finalizing' AND finalizing_at IS NULL THEN NOW() ELSE finalizing_at END,
		cancelling_at=CASE WHEN $1='cancelling' AND cancelling_at IS NULL THEN NOW() ELSE cancelling_at END,
		completed_at=CASE WHEN $1='completed' THEN COALESCE(completed_at,NOW()) ELSE completed_at END,
		cancelled_at=CASE WHEN $1='cancelled' THEN COALESCE(cancelled_at,NOW()) ELSE cancelled_at END,
		expired_at=CASE WHEN $1='expired' THEN COALESCE(expired_at,NOW()) ELSE expired_at END,
		failed_at=CASE WHEN $1='failed' THEN COALESCE(failed_at,NOW()) ELSE failed_at END,
		version=version+1,updated_at=NOW() WHERE id=$5 AND user_id=$6 AND api_key_id=$7 AND version=$8`, status, retryAt, errCode, errMessage, id, userID, apiKeyID, version)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ResetBatchForRetry atomically reopens a failed or expired batch and puts all
// unfinished items back on the pending queue. Succeeded items remain durable,
// so a retry cannot duplicate work that already completed successfully.
func (r *ManagedBatchRepository) ResetBatchForRetry(ctx context.Context, userID, apiKeyID int64, id string, version int64) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE batches SET status='validating',retry_at=NULL,last_error_code=NULL,last_error_message=NULL,in_progress_at=NULL,finalizing_at=NULL,cancelling_at=NULL,completed_at=NULL,cancelled_at=NULL,expired_at=NULL,failed_at=NULL,request_counts=(SELECT jsonb_build_object('total',COUNT(*),'completed',COUNT(*) FILTER (WHERE status='succeeded'),'failed',0) FROM batch_items WHERE batch_id=$5),version=version+1,updated_at=NOW() WHERE id=$1 AND user_id=$2 AND api_key_id=$3 AND version=$4 AND status IN ('failed','expired')`, id, userID, apiKeyID, version, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE batch_items SET status='pending',response=NULL,error=NULL,unknown_result=FALSE,next_attempt_at=NULL,lease_owner=NULL,lease_expires_at=NULL,completed_at=NULL,updated_at=NOW() WHERE batch_id=$1 AND status IN ('pending','retry','failed','unknown')`, id); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO batch_outbox (batch_id,event_type,payload,dedup_key,available_at) VALUES ($1,'batch.submit',jsonb_build_object('batch_id',$1,'user_id',$2,'api_key_id',$3),$4,NOW()) ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`, id, userID, apiKeyID, "batch.retry:"+id+":"+fmt.Sprint(version+1)); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
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
		SELECT id FROM batch_items WHERE batch_id=$1 AND status IN ('pending','retry') AND (next_attempt_at IS NULL OR next_attempt_at<=NOW()) AND (lease_expires_at IS NULL OR lease_expires_at<NOW()) AND EXISTS (SELECT 1 FROM batches WHERE id=$1 AND status IN ('validating','in_progress')) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $3
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

// RetryItem releases an item lease without marking the request terminal.  It
// is deliberately fenced by lease_owner so a worker that was killed and later
// resumed cannot put a newer attempt back on the retry queue.
func (r *ManagedBatchRepository) RetryItem(ctx context.Context, id int64, worker string, next time.Time, itemErr map[string]any) (bool, error) {
	eraw, _ := json.Marshal(itemErr)
	res, err := r.db.ExecContext(ctx, `UPDATE batch_items SET status='retry',next_attempt_at=$1,error=$2::jsonb,lease_owner=NULL,lease_expires_at=NULL,updated_at=NOW() WHERE id=$3 AND lease_owner=$4`, next, eraw, id, worker)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// CancelPendingItems fences work that has not started when a batch enters
// cancelling. Running items keep their lease and may finish; reconciliation
// publishes the batch's cancelled terminal state after they drain.
func (r *ManagedBatchRepository) CancelPendingItems(ctx context.Context, batchID string) (int, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("managed batch repository: nil db")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE batch_items SET status='cancelled',error=$2::jsonb,completed_at=NOW(),updated_at=NOW() WHERE batch_id=$1 AND status IN ('pending','retry')`, batchID, `{"code":"cancelled","message":"batch was cancelled before execution"}`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

type ManagedBatchProgress struct {
	Total     int
	Succeeded int
	Failed    int
	Cancelled int
	Unknown   int
	Pending   int
	Running   int
	Retry     int
}

// UpdateBatchProgress publishes the item aggregate used by the public Batch
// object. Item rows remain the source of truth; this projection is updated
// after each worker cycle so status reads do not need to scan all items.
func (r *ManagedBatchRepository) UpdateBatchProgress(ctx context.Context, batchID string, p ManagedBatchProgress) error {
	completed := p.Succeeded + p.Failed + p.Cancelled + p.Unknown
	failed := p.Failed + p.Unknown
	_, err := r.db.ExecContext(ctx, `UPDATE batches SET request_counts=$1::jsonb,updated_at=NOW() WHERE id=$2`,
		fmt.Sprintf(`{"total":%d,"completed":%d,"failed":%d}`, p.Total, completed, failed), batchID)
	return err
}

// CountBatchItems is a read-only reconciliation snapshot. It intentionally
// reports unknown separately: an unknown provider outcome must never be
// silently counted as success.
func (r *ManagedBatchRepository) CountBatchItems(ctx context.Context, batchID string) (ManagedBatchProgress, error) {
	var p ManagedBatchProgress
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(*) FILTER (WHERE status='succeeded'),COUNT(*) FILTER (WHERE status='failed'),COUNT(*) FILTER (WHERE status='cancelled'),COUNT(*) FILTER (WHERE status='unknown'),COUNT(*) FILTER (WHERE status='pending'),COUNT(*) FILTER (WHERE status='running'),COUNT(*) FILTER (WHERE status='retry') FROM batch_items WHERE batch_id=$1`, batchID).Scan(&p.Total, &p.Succeeded, &p.Failed, &p.Cancelled, &p.Unknown, &p.Pending, &p.Running, &p.Retry)
	return p, err
}

// CompareAndSwapBatchStatusWithOutbox makes the terminal state transition and
// its notification one durable transaction. The event is inserted only when
// the version fence succeeds; a stale worker therefore cannot emit a second
// completion event.
func (r *ManagedBatchRepository) CompareAndSwapBatchStatusWithOutbox(ctx context.Context, userID, apiKeyID int64, id string, version int64, status string, retryAt *time.Time, errCode, errMessage *string, event *ManagedBatchOutboxEvent) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE batches SET status=$1,retry_at=$2,last_error_code=$3,last_error_message=$4,
		in_progress_at=CASE WHEN $1='in_progress' AND in_progress_at IS NULL THEN NOW() ELSE in_progress_at END,
		finalizing_at=CASE WHEN $1='finalizing' AND finalizing_at IS NULL THEN NOW() ELSE finalizing_at END,
		cancelling_at=CASE WHEN $1='cancelling' AND cancelling_at IS NULL THEN NOW() ELSE cancelling_at END,
		completed_at=CASE WHEN $1='completed' THEN COALESCE(completed_at,NOW()) ELSE completed_at END,
		cancelled_at=CASE WHEN $1='cancelled' THEN COALESCE(cancelled_at,NOW()) ELSE cancelled_at END,
		expired_at=CASE WHEN $1='expired' THEN COALESCE(expired_at,NOW()) ELSE expired_at END,
		failed_at=CASE WHEN $1='failed' THEN COALESCE(failed_at,NOW()) ELSE failed_at END,
		version=version+1,updated_at=NOW() WHERE id=$5 AND user_id=$6 AND api_key_id=$7 AND version=$8`, status, retryAt, errCode, errMessage, id, userID, apiKeyID, version)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if event != nil {
		payload, marshalErr := json.Marshal(event.Payload)
		if marshalErr != nil {
			return false, marshalErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO batch_outbox (batch_id,event_type,payload,dedup_key,available_at) VALUES ($1,$2,$3::jsonb,$4,COALESCE($5,NOW())) ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`, event.BatchID, event.EventType, payload, event.DedupKey, event.AvailableAt); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
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

package handler

// This file is the narrow bridge between the OpenAI-compatible HTTP contract
// and the durable managed-batch repositories. Provider selection and execution
// remain worker concerns; submit therefore leaves the batch in validating and
// hands the work to the durable outbox.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

// ManagedBatchObjectStore is deliberately smaller than any vendor SDK. The
// production adapter can wrap the existing S3 backup store; tests use a tiny
// in-memory implementation without copying S3 client code.
type ManagedBatchObjectStore interface {
	Upload(context.Context, string, io.Reader, string) (int64, error)
	Download(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

type ManagedBatchRepositoryPort interface {
	CreateFile(context.Context, repository.ManagedBatchFile) error
	GetFileForOwner(context.Context, int64, int64, string) (*repository.ManagedBatchFile, error)
	ListFilesForOwner(context.Context, int64, int64, string, string, int) ([]repository.ManagedBatchFile, bool, error)
	DeleteFileForOwner(context.Context, int64, int64, string) error
	CreateBatchSubmission(context.Context, repository.ManagedBatchSubmission) (*repository.ManagedBatch, error)
	GetBatchForOwner(context.Context, int64, int64, string) (*repository.ManagedBatch, error)
	ListBatchesForOwner(context.Context, int64, int64, string, int) ([]repository.ManagedBatch, bool, error)
	CompareAndSwapBatchStatus(context.Context, int64, int64, string, int64, string, *time.Time, *string, *string) (bool, error)
	CancelPendingItems(context.Context, string) (int, error)
	ResetBatchForRetry(context.Context, int64, int64, string, int64) (bool, error)
	EnqueueOutbox(context.Context, repository.ManagedBatchOutboxEvent) error
}

type ManagedBatchRepositoryBackend struct {
	Repo  ManagedBatchRepositoryPort
	Store ManagedBatchObjectStore
	Queue service.ManagedBatchQueue // reserved for the worker wiring; submit uses outbox first
}

func NewManagedBatchRepositoryBackend(repo ManagedBatchRepositoryPort, store ManagedBatchObjectStore, queue service.ManagedBatchQueue) *ManagedBatchRepositoryBackend {
	return &ManagedBatchRepositoryBackend{Repo: repo, Store: store, Queue: queue}
}

func (b *ManagedBatchRepositoryBackend) unavailable() error {
	return &BatchAPIError{Status: 503, Type: "server_error", Code: "batch_backend_unavailable", Message: "batch backend is not configured"}
}
func (b *ManagedBatchRepositoryBackend) ensure() error {
	if b == nil || b.Repo == nil || b.Store == nil {
		return b.unavailable()
	}
	return nil
}
func newManagedID(prefix string) string { return prefix + uuid.NewString() }

func (b *ManagedBatchRepositoryBackend) CreateFile(ctx context.Context, owner ManagedBatchOwner, filename, purpose string, size int64, body io.Reader) (ManagedBatchFile, error) {
	if err := b.ensure(); err != nil {
		return ManagedBatchFile{}, err
	}
	id, key := newManagedID("file_"), "managed-batches/"+fmt.Sprint(owner.UserID)+"/"+newManagedID("obj_")
	h := sha256.New()
	n, err := b.Store.Upload(ctx, key, io.TeeReader(body, h), "application/jsonl")
	if err != nil {
		return ManagedBatchFile{}, &BatchAPIError{Status: 503, Type: "server_error", Code: "storage_unavailable", Message: "file storage is unavailable"}
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if size < 0 {
		size = n
	}
	f := repository.ManagedBatchFile{ID: id, UserID: owner.UserID, APIKeyID: owner.APIKeyID, Purpose: purpose, Filename: filename, StorageRef: key, ByteSize: size, SHA256: &digest, Status: batchcontract.FileStatusUploaded, ExpiresAt: timePtr(time.Now().Add(24 * time.Hour))}
	if err := b.Repo.CreateFile(ctx, f); err != nil {
		_ = b.Store.Delete(ctx, key)
		return ManagedBatchFile{}, err
	}
	return ManagedBatchFile{Object: fileObject(f), StorageRef: key}, nil
}

func (b *ManagedBatchRepositoryBackend) GetFile(ctx context.Context, owner ManagedBatchOwner, id string) (ManagedBatchFile, error) {
	if err := b.ensure(); err != nil {
		return ManagedBatchFile{}, err
	}
	f, err := b.Repo.GetFileForOwner(ctx, owner.UserID, owner.APIKeyID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedBatchFile{}, ErrManagedBatchNotFound
	}
	if err != nil {
		return ManagedBatchFile{}, err
	}
	return ManagedBatchFile{Object: fileObject(*f), StorageRef: f.StorageRef}, nil
}
func (b *ManagedBatchRepositoryBackend) ListFiles(ctx context.Context, owner ManagedBatchOwner, purpose, after string, limit int) (batchcontract.FileList, error) {
	if err := b.ensure(); err != nil {
		return batchcontract.FileList{}, err
	}
	files, more, err := b.Repo.ListFilesForOwner(ctx, owner.UserID, owner.APIKeyID, purpose, after, limit)
	if err != nil {
		return batchcontract.FileList{}, err
	}
	out := batchcontract.FileList{Object: "list", HasMore: more}
	for _, f := range files {
		out.Data = append(out.Data, fileObject(f))
	}
	if len(out.Data) > 0 {
		out.FirstID = &out.Data[0].ID
		out.LastID = &out.Data[len(out.Data)-1].ID
	}
	return out, nil
}
func (b *ManagedBatchRepositoryBackend) OpenFile(ctx context.Context, owner ManagedBatchOwner, id string) (io.ReadCloser, *batchcontract.FileObject, error) {
	f, err := b.GetFile(ctx, owner, id)
	if err != nil {
		return nil, nil, err
	}
	stream, err := b.Store.Download(ctx, f.StorageRef)
	if err != nil {
		return nil, nil, &BatchAPIError{Status: 503, Type: "server_error", Code: "storage_unavailable", Message: "file storage is unavailable"}
	}
	return stream, &f.Object, nil
}
func (b *ManagedBatchRepositoryBackend) DeleteFile(ctx context.Context, owner ManagedBatchOwner, id string) error {
	if err := b.ensure(); err != nil {
		return err
	}
	f, err := b.Repo.GetFileForOwner(ctx, owner.UserID, owner.APIKeyID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrManagedBatchNotFound
	}
	if err != nil {
		return err
	}
	if err = b.Repo.DeleteFileForOwner(ctx, owner.UserID, owner.APIKeyID, id); errors.Is(err, sql.ErrNoRows) {
		return ErrManagedBatchNotFound
	}
	if err != nil {
		return err
	}
	if err = b.Store.Delete(ctx, f.StorageRef); err != nil {
		return &BatchAPIError{Status: 503, Type: "server_error", Code: "storage_unavailable", Message: "file storage is unavailable"}
	}
	return nil
}

func (b *ManagedBatchRepositoryBackend) CreateBatch(ctx context.Context, owner ManagedBatchOwner, req batchcontract.BatchSubmitRequest, lines []batchcontract.RequestLine, idem, hash string) (batchcontract.Batch, error) {
	if err := b.ensure(); err != nil {
		return batchcontract.Batch{}, err
	}
	if req.Endpoint != batchcontract.EndpointResponses {
		return batchcontract.Batch{}, &BatchAPIError{Status: 400, Type: "invalid_request_error", Code: batchcontract.ErrorInvalidEndpoint, Message: "managed Batch currently supports only /v1/responses"}
	}
	if err := batchcontract.ValidateRequestLines(lines, req.Endpoint); err != nil {
		return batchcontract.Batch{}, err
	}
	id := newManagedID("batch_")
	now := time.Now().UTC()
	deadline := now.Add(24 * time.Hour)
	counts := map[string]any{"total": len(lines), "completed": 0, "failed": 0}
	metadata := map[string]any{}
	for k, v := range req.Metadata {
		metadata[k] = v
	}
	items := make([]repository.ManagedBatchItem, 0, len(lines))
	for _, line := range lines {
		if !json.Valid(line.Body) {
			return batchcontract.Batch{}, &BatchAPIError{Status: 400, Type: "invalid_request_error", Code: "invalid_request", Message: "invalid request body"}
		}
		request := map[string]any{
			"custom_id": line.CustomID,
			"method":    line.Method,
			"url":       line.URL,
			"body":      json.RawMessage(line.Body),
		}
		items = append(items, repository.ManagedBatchItem{BatchID: id, CustomID: line.CustomID, Request: request, Status: batchcontract.ItemStatusPending})
	}
	mb := repository.ManagedBatch{ID: id, UserID: owner.UserID, APIKeyID: owner.APIKeyID, InputFileID: req.InputFileID, Endpoint: req.Endpoint, CompletionWindow: req.CompletionWindow, Status: batchcontract.BatchStatusValidating, RequestCounts: counts, Metadata: metadata, DeadlineAt: &deadline, Version: 0, IdempotencyKey: strings.TrimSpace(idem), RequestHash: hash, CreatedAt: now}
	persisted, err := b.Repo.CreateBatchSubmission(ctx, repository.ManagedBatchSubmission{Batch: mb, Items: items, Outbox: repository.ManagedBatchOutboxEvent{BatchID: id, EventType: "batch.submit", DedupKey: "batch.submit:" + id, Payload: map[string]any{
		"batch_id":   id,
		"user_id":    owner.UserID,
		"api_key_id": owner.APIKeyID,
	}}})
	if err != nil {
		if errors.Is(err, repository.ErrManagedBatchIdempotencyConflict) {
			return batchcontract.Batch{}, &BatchAPIError{Status: 409, Type: "invalid_request_error", Code: batchcontract.ErrorIdempotencyConflict, Message: "idempotency key was already used with a different request"}
		}
		return batchcontract.Batch{}, err
	}
	if persisted == nil {
		return batchcontract.Batch{}, errors.New("managed batch repository returned no batch")
	}
	return publicBatch(*persisted), nil
}

func (b *ManagedBatchRepositoryBackend) GetBatch(ctx context.Context, owner ManagedBatchOwner, id string) (batchcontract.Batch, error) {
	if err := b.ensure(); err != nil {
		return batchcontract.Batch{}, err
	}
	mb, err := b.Repo.GetBatchForOwner(ctx, owner.UserID, owner.APIKeyID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return batchcontract.Batch{}, ErrManagedBatchNotFound
	}
	if err != nil {
		return batchcontract.Batch{}, err
	}
	return publicBatch(*mb), nil
}
func (b *ManagedBatchRepositoryBackend) ListBatches(ctx context.Context, owner ManagedBatchOwner, after string, limit int) (batchcontract.BatchList, error) {
	if err := b.ensure(); err != nil {
		return batchcontract.BatchList{}, err
	}
	rows, more, err := b.Repo.ListBatchesForOwner(ctx, owner.UserID, owner.APIKeyID, after, limit)
	if err != nil {
		return batchcontract.BatchList{}, err
	}
	out := batchcontract.BatchList{Object: "list", HasMore: more}
	for _, mb := range rows {
		out.Data = append(out.Data, publicBatch(mb))
	}
	if len(out.Data) > 0 {
		out.FirstID = &out.Data[0].ID
		out.LastID = &out.Data[len(out.Data)-1].ID
	}
	return out, nil
}
func (b *ManagedBatchRepositoryBackend) CancelBatch(ctx context.Context, owner ManagedBatchOwner, id string) (batchcontract.Batch, error) {
	if err := b.ensure(); err != nil {
		return batchcontract.Batch{}, err
	}
	mb, err := b.loadBatch(ctx, owner, id)
	if err != nil {
		return batchcontract.Batch{}, err
	}
	if err := batchcontract.TransitionBatch(mb.Status, batchcontract.BatchStatusCancelling); err != nil {
		return batchcontract.Batch{}, batchConflict(err)
	}
	ok, err := b.Repo.CompareAndSwapBatchStatus(ctx, owner.UserID, owner.APIKeyID, id, mb.Version, batchcontract.BatchStatusCancelling, nil, nil, nil)
	if err != nil {
		return batchcontract.Batch{}, err
	}
	if !ok {
		return batchcontract.Batch{}, batchConflict(errors.New("batch changed concurrently"))
	}
	if _, err := b.Repo.CancelPendingItems(ctx, id); err != nil {
		return batchcontract.Batch{}, err
	}
	mb.Status = batchcontract.BatchStatusCancelling
	mb.Version++
	return publicBatch(*mb), nil
}
func (b *ManagedBatchRepositoryBackend) RetryBatch(ctx context.Context, owner ManagedBatchOwner, id, _ string) (batchcontract.Batch, error) {
	if err := b.ensure(); err != nil {
		return batchcontract.Batch{}, err
	}
	mb, err := b.loadBatch(ctx, owner, id)
	if err != nil {
		return batchcontract.Batch{}, err
	}
	if mb.Status != batchcontract.BatchStatusFailed && mb.Status != batchcontract.BatchStatusExpired {
		return batchcontract.Batch{}, batchConflict(fmt.Errorf("batch cannot be retried from %q", mb.Status))
	}
	ok, err := b.Repo.ResetBatchForRetry(ctx, owner.UserID, owner.APIKeyID, id, mb.Version)
	if err != nil {
		return batchcontract.Batch{}, err
	}
	if !ok {
		return batchcontract.Batch{}, batchConflict(errors.New("batch changed concurrently"))
	}
	mb.Status = batchcontract.BatchStatusValidating
	mb.Version++
	return publicBatch(*mb), nil
}
func (b *ManagedBatchRepositoryBackend) loadBatch(ctx context.Context, owner ManagedBatchOwner, id string) (*repository.ManagedBatch, error) {
	mb, err := b.Repo.GetBatchForOwner(ctx, owner.UserID, owner.APIKeyID, id)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, io.EOF) {
		return nil, ErrManagedBatchNotFound
	}
	return mb, err
}

func batchConflict(err error) error {
	return &BatchAPIError{Status: 409, Type: "invalid_request_error", Code: "conflict", Message: err.Error()}
}

func fileObject(f repository.ManagedBatchFile) batchcontract.FileObject {
	created := f.CreatedAt.Unix()
	if f.CreatedAt.IsZero() {
		created = time.Now().Unix()
	}
	return batchcontract.FileObject{ID: f.ID, Object: "file", Bytes: f.ByteSize, CreatedAt: created, Filename: f.Filename, Purpose: f.Purpose, Status: f.Status}
}
func publicBatch(b repository.ManagedBatch) batchcontract.Batch {
	m := map[string]string{}
	for k, v := range b.Metadata {
		if s, ok := v.(string); ok {
			m[k] = s
		}
	}
	out := batchcontract.Batch{ID: b.ID, Object: "batch", Endpoint: b.Endpoint, CompletionWindow: b.CompletionWindow, Metadata: m, Status: b.Status, InputFileID: b.InputFileID, OutputFileID: b.OutputFileID, ErrorFileID: b.ErrorFileID, RequestCounts: batchcontract.RequestCounts{Total: intValue(b.RequestCounts["total"]), Completed: intValue(b.RequestCounts["completed"]), Failed: intValue(b.RequestCounts["failed"])}, CreatedAt: b.CreatedAt.Unix()}
	for _, item := range []struct {
		value  *time.Time
		target **int64
	}{{b.InProgressAt, &out.InProgressAt}, {b.FinalizingAt, &out.FinalizingAt}, {b.CancellingAt, &out.CancellingAt}, {b.CompletedAt, &out.CompletedAt}, {b.CancelledAt, &out.CancelledAt}, {b.ExpiredAt, &out.ExpiredAt}, {b.FailedAt, &out.FailedAt}} {
		if item.value != nil {
			v := item.value.Unix()
			*item.target = &v
		}
	}
	if b.DeadlineAt != nil {
		expires := b.DeadlineAt.Unix()
		out.ExpiresAt = &expires
	}
	return out
}
func intValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}
func timePtr(t time.Time) *time.Time { return &t }

var _ ManagedBatchBackend = (*ManagedBatchRepositoryBackend)(nil)

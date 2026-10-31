// Package batchworker connects the durable Batch repository to the existing
// queue worker.  It intentionally stops at a narrow provider runner seam:
// account selection, protocol routing, failover and billing remain owners of
// the normal gateway path.
package batchworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/batchadapter"
	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	itemStatusRetry   = "retry"
	itemStatusUnknown = batchcontract.ItemStatusUnknown
)

var (
	ErrInvalidJobPayload   = errors.New("managed batch worker: invalid job payload")
	ErrExecutorUnavailable = errors.New("managed batch worker: executor unavailable")
	ErrResultIndex         = errors.New("managed batch worker: result index failed")
)

// JobPayload is persisted in the queue outbox. AccountID is selected by the
// normal gateway scheduler before enqueue; this package never chooses an
// account or performs failover.
type JobPayload struct {
	BatchID   string `json:"batch_id"`
	UserID    int64  `json:"user_id"`
	APIKeyID  int64  `json:"api_key_id"`
	Provider  string `json:"provider"`
	AccountID int64  `json:"account_id"`
}

type Repository interface {
	GetBatchForOwner(context.Context, int64, int64, string) (*repository.ManagedBatch, error)
	ClaimItems(context.Context, string, string, int, time.Duration) ([]repository.ManagedBatchItem, error)
	CompleteItem(context.Context, int64, string, string, map[string]any, map[string]any, int64, int64, bool) (bool, error)
	RetryItem(context.Context, int64, string, time.Time, map[string]any) (bool, error)
	CountBatchItems(context.Context, string) (repository.ManagedBatchProgress, error)
	CompareAndSwapBatchStatusWithOutbox(context.Context, int64, int64, string, int64, string, *time.Time, *string, *string, *repository.ManagedBatchOutboxEvent) (bool, error)
}

// ResultIndexer is deliberately small and idempotent by (batch,custom_id).
// A durable implementation writes the OpenAI-compatible JSONL result file;
// tests may use an in-memory fixture. The processor does not claim completion
// until indexing succeeds.
type ResultIndexer interface {
	Index(context.Context, string, batchcontract.ResultLine) error
}

type Config struct {
	Enabled       bool
	WorkerID      string
	BatchSize     int
	ItemLeaseTTL  time.Duration
	RetryDelay    time.Duration
	ResultIndexer ResultIndexer
}

func (c Config) withDefaults() Config {
	if c.WorkerID == "" {
		c.WorkerID = "managed-batch-worker"
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	if c.ItemLeaseTTL <= 0 {
		c.ItemLeaseTTL = 5 * time.Minute
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = time.Minute
	}
	return c
}

// NewProcessor returns nil while disabled. Construction has no goroutines or
// network side effects; the caller must explicitly pass the processor to the
// queue worker when rollout is enabled.
func NewProcessor(cfg Config, repo Repository, executors map[string]batchadapter.ManagedExecutor) *Processor {
	cfg = cfg.withDefaults()
	if !cfg.Enabled || repo == nil {
		return nil
	}
	return &Processor{repo: repo, executors: executors, cfg: cfg}
}

type Processor struct {
	repo      Repository
	executors map[string]batchadapter.ManagedExecutor
	cfg       Config
}

func (p *Processor) Process(ctx context.Context, job service.ManagedBatchJob) (service.ManagedBatchProcessResult, error) {
	if p == nil || p.repo == nil {
		return service.ManagedBatchProcessResult{Terminal: true}, ErrExecutorUnavailable
	}
	var payload JobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.BatchID == "" || payload.UserID <= 0 || payload.Provider == "" {
		return service.ManagedBatchProcessResult{Terminal: true}, ErrInvalidJobPayload
	}
	b, err := p.repo.GetBatchForOwner(ctx, payload.UserID, payload.APIKeyID, payload.BatchID)
	if err != nil {
		return service.ManagedBatchProcessResult{}, err
	}
	if batchcontract.IsBatchTerminal(b.Status) {
		return service.ManagedBatchProcessResult{Terminal: true}, nil
	}
	persistCtx := context.WithoutCancel(ctx)
	if b.DeadlineAt != nil && !time.Now().Before(*b.DeadlineAt) {
		_, err := p.transition(persistCtx, payload, b, batchcontract.BatchStatusExpired, batchcontract.ErrorExpired, "batch deadline exceeded", nil)
		return service.ManagedBatchProcessResult{Terminal: true}, err
	}
	if b.Status == batchcontract.BatchStatusValidating {
		changed, err := p.transition(persistCtx, payload, b, batchcontract.BatchStatusInProgress, "", "", nil)
		if err != nil {
			return service.ManagedBatchProcessResult{}, err
		}
		if changed {
			b.Version++
			b.Status = batchcontract.BatchStatusInProgress
		}
	}
	if b.Status == batchcontract.BatchStatusCancelling {
		return p.reconcile(persistCtx, payload, b)
	}
	items, err := p.repo.ClaimItems(ctx, payload.BatchID, p.cfg.WorkerID, p.cfg.BatchSize, p.cfg.ItemLeaseTTL)
	if err != nil {
		return service.ManagedBatchProcessResult{}, err
	}
	for i := range items {
		if err := p.processItem(ctx, persistCtx, payload, items[i], b.Endpoint); err != nil {
			// A context cancellation after a provider call is represented as an
			// unknown item outcome; the durable state is already fenced.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return service.ManagedBatchProcessResult{}, err
			}
			return service.ManagedBatchProcessResult{}, err
		}
	}
	return p.reconcile(persistCtx, payload, b)
}

func (p *Processor) processItem(ctx, persistCtx context.Context, payload JobPayload, item repository.ManagedBatchItem, endpoint string) error {
	line, err := requestLine(item.Request)
	if err != nil {
		_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusFailed, nil, errorMap(batchcontract.ErrorInvalidRequest, err.Error()), 0, 0, false)
		return completeErr
	}
	executor := p.executors[payload.Provider]
	if executor == nil || !executor.Capability().Managed {
		_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusFailed, nil, errorMap(batchcontract.ErrorUnsupportedMode, ErrExecutorUnavailable.Error()), 0, 0, false)
		return completeErr
	}
	if err := executor.ValidateRequest(line, endpoint); err != nil {
		_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusFailed, nil, errorMap(batchcontract.ErrorInvalidRequest, err.Error()), 0, 0, false)
		return completeErr
	}
	model := ""
	var bodyMap map[string]any
	if err := json.Unmarshal(line.Body, &bodyMap); err == nil {
		if value, ok := bodyMap["model"].(string); ok {
			model = value
		}
	}
	if ctx.Err() != nil {
		_, retryErr := p.repo.RetryItem(persistCtx, item.ID, p.cfg.WorkerID, time.Now().Add(p.cfg.RetryDelay), errorMap("cancelled_before_start", ctx.Err().Error()))
		return errors.Join(ctx.Err(), retryErr)
	}
	result, execErr := executor.Execute(ctx, batchadapter.ManagedRequest{AccountID: payload.AccountID, CustomID: item.CustomID, Endpoint: endpoint, Method: line.Method, Model: model, Body: line.Body})
	if execErr != nil {
		if errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) {
			return p.completeUnknown(persistCtx, payload.BatchID, item, execErr)
		}
		var providerErr *batchadapter.ProviderError
		if errors.As(execErr, &providerErr) && providerErr.Class == batchadapter.ErrorRetryable {
			_, retryErr := p.repo.RetryItem(persistCtx, item.ID, p.cfg.WorkerID, time.Now().Add(p.cfg.RetryDelay), errorMap(providerErr.Code, providerErr.Message))
			return retryErr
		}
		_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusFailed, nil, errorMap("provider_error", execErr.Error()), 0, 0, false)
		return completeErr
	}
	if result.Error != nil {
		pe := result.Error
		if pe.Class == batchadapter.ErrorRetryable {
			_, retryErr := p.repo.RetryItem(persistCtx, item.ID, p.cfg.WorkerID, time.Now().Add(p.cfg.RetryDelay), errorMap(pe.Code, pe.Message))
			return retryErr
		}
		lineResult := batchcontract.ResultLine{ID: strconv.FormatInt(item.ID, 10), CustomID: item.CustomID, Error: &batchcontract.BatchError{Code: pe.Code, Message: pe.Message}}
		if err := p.index(persistCtx, payload.BatchID, lineResult); err != nil {
			return err
		}
		_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusFailed, nil, errorMap(pe.Code, pe.Message), int64(result.Usage.InputTokens), int64(result.Usage.OutputTokens), false)
		return completeErr
	}
	lineResult := batchcontract.ResultLine{ID: strconv.FormatInt(item.ID, 10), CustomID: item.CustomID, Response: &result.Response}
	if err := p.index(persistCtx, payload.BatchID, lineResult); err != nil {
		return p.completeUnknown(persistCtx, payload.BatchID, item, err)
	}
	_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusSucceeded, map[string]any{"status_code": result.Response.StatusCode, "request_id": result.Response.RequestID, "body": json.RawMessage(result.Response.Body)}, nil, int64(result.Usage.InputTokens), int64(result.Usage.OutputTokens), false)
	return completeErr
}

func (p *Processor) completeUnknown(ctx context.Context, batchID string, item repository.ManagedBatchItem, cause error) error {
	line := batchcontract.ResultLine{ID: strconv.FormatInt(item.ID, 10), CustomID: item.CustomID, Error: &batchcontract.BatchError{Code: batchcontract.ErrorUnknownOutcome, Message: cause.Error()}}
	indexErr := p.index(ctx, batchID, line)
	_, completeErr := p.repo.CompleteItem(ctx, item.ID, p.cfg.WorkerID, itemStatusUnknown, nil, errorMap(batchcontract.ErrorUnknownOutcome, cause.Error()), 0, 0, true)
	return errors.Join(indexErr, completeErr)
}

func (p *Processor) index(ctx context.Context, batchID string, line batchcontract.ResultLine) error {
	if p.cfg.ResultIndexer == nil {
		return nil
	}
	if err := p.cfg.ResultIndexer.Index(ctx, batchID, line); err != nil {
		return fmt.Errorf("%w: %v", ErrResultIndex, err)
	}
	return nil
}

func (p *Processor) reconcile(ctx context.Context, payload JobPayload, b *repository.ManagedBatch) (service.ManagedBatchProcessResult, error) {
	progress, err := p.repo.CountBatchItems(ctx, payload.BatchID)
	if err != nil {
		return service.ManagedBatchProcessResult{}, err
	}
	if progress.Pending > 0 || progress.Retry > 0 || progress.Running > 0 {
		return service.ManagedBatchProcessResult{RequeueAfter: p.cfg.RetryDelay}, nil
	}
	status := batchcontract.BatchStatusCompleted
	code, message := "", ""
	if b.Status == batchcontract.BatchStatusCancelling {
		status = batchcontract.BatchStatusCancelled
	} else if progress.Unknown > 0 {
		status, code, message = batchcontract.BatchStatusFailed, batchcontract.ErrorUnknownOutcome, "one or more provider outcomes are unknown"
	} else if progress.Failed > 0 {
		status, code, message = batchcontract.BatchStatusFailed, "item_failed", "one or more batch items failed"
	}
	changed, err := p.transition(ctx, payload, b, status, code, message, &repository.ManagedBatchOutboxEvent{BatchID: payload.BatchID, EventType: "batch." + status, DedupKey: payload.BatchID + ":" + status, Payload: map[string]any{"batch_id": payload.BatchID, "status": status}})
	return service.ManagedBatchProcessResult{Terminal: changed && err == nil, RequeueAfter: p.cfg.RetryDelay}, err
}

func (p *Processor) transition(ctx context.Context, payload JobPayload, b *repository.ManagedBatch, status, code, message string, event *repository.ManagedBatchOutboxEvent) (bool, error) {
	var codePtr, messagePtr *string
	if code != "" {
		codePtr = &code
	}
	if message != "" {
		messagePtr = &message
	}
	return p.repo.CompareAndSwapBatchStatusWithOutbox(ctx, payload.UserID, payload.APIKeyID, payload.BatchID, b.Version, status, nil, codePtr, messagePtr, event)
}

func requestLine(request map[string]any) (batchcontract.RequestLine, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return batchcontract.RequestLine{}, err
	}
	var line batchcontract.RequestLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return line, err
	}
	if line.CustomID == "" || line.Method != http.MethodPost || line.URL == "" || len(line.Body) == 0 {
		return line, ErrInvalidJobPayload
	}
	return line, nil
}

func errorMap(code, message string) map[string]any {
	return map[string]any{"code": code, "message": message}
}

var _ service.ManagedBatchProcessor = (*Processor)(nil)

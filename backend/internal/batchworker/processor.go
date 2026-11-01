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

// JobPayload is persisted in the queue outbox. Provider/AccountID may be
// filled by a reviewed execution resolver; the worker itself never chooses an
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
	CancelPendingItems(context.Context, string) (int, error)
	CountBatchItems(context.Context, string) (repository.ManagedBatchProgress, error)
	CompareAndSwapBatchStatusWithOutbox(context.Context, int64, int64, string, int64, string, *time.Time, *string, *string, *repository.ManagedBatchOutboxEvent) (bool, error)
}

// ProgressUpdater is optional for test repositories, but required by the
// production repository so public Batch request_counts stays current.
type ProgressUpdater interface {
	UpdateBatchProgress(context.Context, string, repository.ManagedBatchProgress) error
}

// ExecutionResolver selects the already-authorized provider/account for one
// item. It is the only seam allowed to connect Batch execution to the normal
// Plan and account scheduler.
type ExecutionResolver interface {
	Resolve(context.Context, batchadapter.ExecutionRequest, batchadapter.ExecutionBatch, batchadapter.ExecutionItem, batchcontract.RequestLine) (batchadapter.ExecutionSelection, error)
}

// ResultIndexer is deliberately small and idempotent by (batch,custom_id).
// A durable implementation writes the OpenAI-compatible JSONL result file;
// tests may use an in-memory fixture. The processor does not claim completion
// until indexing succeeds.
type ResultIndexer interface {
	Index(context.Context, string, batchcontract.ResultLine) error
}

// ResultFinalizer materializes the durable item results into the public
// OpenAI-compatible output/error JSONL files. It runs only after every item is
// terminal and must be idempotent because queue delivery is at-least-once.
type ResultFinalizer interface {
	Finalize(context.Context, string) error
}

type Config struct {
	Enabled         bool
	WorkerID        string
	BatchSize       int
	ItemLeaseTTL    time.Duration
	RetryDelay      time.Duration
	ResultIndexer   ResultIndexer
	ResultFinalizer ResultFinalizer
	Resolver        ExecutionResolver
	Settlement      service.ManagedBatchSettlement
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
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.BatchID == "" || payload.UserID <= 0 {
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
		changed, err := p.transition(persistCtx, payload, b, batchcontract.BatchStatusExpired, batchcontract.ErrorExpired, "batch deadline exceeded", nil)
		if err != nil {
			return service.ManagedBatchProcessResult{}, err
		}
		if changed {
			return service.ManagedBatchProcessResult{Terminal: true}, nil
		}
		fresh, err := p.repo.GetBatchForOwner(persistCtx, payload.UserID, payload.APIKeyID, payload.BatchID)
		if err != nil {
			return service.ManagedBatchProcessResult{}, err
		}
		if batchcontract.IsBatchTerminal(fresh.Status) {
			return service.ManagedBatchProcessResult{Terminal: true}, nil
		}
		return service.ManagedBatchProcessResult{RequeueAfter: p.cfg.RetryDelay}, nil
	}
	if b.Status == batchcontract.BatchStatusValidating {
		changed, err := p.transition(persistCtx, payload, b, batchcontract.BatchStatusInProgress, "", "", nil)
		if err != nil {
			return service.ManagedBatchProcessResult{}, err
		}
		if changed {
			b.Version++
			b.Status = batchcontract.BatchStatusInProgress
		} else {
			fresh, err := p.repo.GetBatchForOwner(persistCtx, payload.UserID, payload.APIKeyID, payload.BatchID)
			if err != nil {
				return service.ManagedBatchProcessResult{}, err
			}
			if batchcontract.IsBatchTerminal(fresh.Status) {
				return service.ManagedBatchProcessResult{Terminal: true}, nil
			}
			b = fresh
			if b.Status != batchcontract.BatchStatusInProgress && b.Status != batchcontract.BatchStatusCancelling && b.Status != batchcontract.BatchStatusFinalizing {
				return service.ManagedBatchProcessResult{RequeueAfter: p.cfg.RetryDelay}, nil
			}
		}
	}
	if b.Status == batchcontract.BatchStatusCancelling {
		if _, err := p.repo.CancelPendingItems(persistCtx, payload.BatchID); err != nil {
			return service.ManagedBatchProcessResult{}, err
		}
		return p.reconcile(persistCtx, payload, b)
	}
	if b.Status == batchcontract.BatchStatusFinalizing {
		return p.reconcile(persistCtx, payload, b)
	}
	if b.Status != batchcontract.BatchStatusInProgress {
		return service.ManagedBatchProcessResult{RequeueAfter: p.cfg.RetryDelay}, nil
	}
	items, err := p.repo.ClaimItems(ctx, payload.BatchID, p.cfg.WorkerID, p.cfg.BatchSize, p.cfg.ItemLeaseTTL)
	if err != nil {
		return service.ManagedBatchProcessResult{}, err
	}
	for i := range items {
		if err := p.processItem(ctx, persistCtx, payload, b, items[i], b.Endpoint); err != nil {
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

func (p *Processor) processItem(ctx, persistCtx context.Context, payload JobPayload, batch *repository.ManagedBatch, item repository.ManagedBatchItem, endpoint string) error {
	line, err := requestLine(item.Request)
	if err != nil {
		_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusFailed, nil, errorMap(batchcontract.ErrorInvalidRequest, err.Error()), 0, 0, false)
		return completeErr
	}
	provider, accountID := payload.Provider, payload.AccountID
	var executionCtx context.Context
	var release func()
	if p.cfg.Resolver != nil {
		var resolveErr error
		selection, resolveErr := p.cfg.Resolver.Resolve(ctx, batchadapter.ExecutionRequest{BatchID: payload.BatchID, UserID: payload.UserID, APIKeyID: payload.APIKeyID, Provider: payload.Provider, AccountID: payload.AccountID}, batchadapter.ExecutionBatch{ID: batch.ID, UserID: batch.UserID, APIKeyID: batch.APIKeyID, Endpoint: batch.Endpoint}, batchadapter.ExecutionItem{ID: item.ID, BatchID: item.BatchID, CustomID: item.CustomID}, line)
		if resolveErr != nil {
			_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusFailed, nil, errorMap(batchcontract.ErrorUnsupportedMode, resolveErr.Error()), 0, 0, false)
			return completeErr
		}
		provider, accountID, executionCtx, release = selection.Provider, selection.AccountID, selection.Context, selection.Release
		if release != nil {
			defer release()
		}
	}
	executor := p.executors[provider]
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
	if accountID <= 0 {
		_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusFailed, nil, errorMap(batchcontract.ErrorUnsupportedMode, "managed batch account was not selected"), 0, 0, false)
		return completeErr
	}
	result, execErr := executor.Execute(ctx, batchadapter.ManagedRequest{ExecutionContext: executionCtx, AccountID: accountID, CustomID: item.CustomID, Endpoint: endpoint, Method: line.Method, Model: model, Body: line.Body})
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
		if p.cfg.ResultFinalizer == nil {
			lineResult := batchcontract.ResultLine{ID: strconv.FormatInt(item.ID, 10), CustomID: item.CustomID, Error: &batchcontract.BatchError{Code: pe.Code, Message: pe.Message}}
			if err := p.index(persistCtx, payload.BatchID, lineResult); err != nil {
				return err
			}
		}
		_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusFailed, nil, errorMap(pe.Code, pe.Message), int64(result.Usage.InputTokens), int64(result.Usage.OutputTokens), false)
		return completeErr
	}
	if p.cfg.Settlement != nil {
		// Settlement runs after the provider returned a definitive success and
		// before the item becomes terminal. The stable item key makes a replay
		// safe even if completion is fenced after billing has committed.
		settleErr := p.cfg.Settlement.SettleManagedBatch(persistCtx, service.ManagedBatchSettlementInput{
			BatchID:                       payload.BatchID,
			ItemID:                        item.ID,
			RequestID:                     managedBatchRequestID(payload.BatchID, item.ID),
			Model:                         firstNonEmptyManagedValue(model, result.Model),
			UpstreamModel:                 result.UpstreamModel,
			BillingModel:                  result.BillingModel,
			ServiceTier:                   result.ServiceTier,
			ReasoningEffort:               result.ReasoningEffort,
			RequestedReasoningEffort:      result.RequestedReasoningEffort,
			UpstreamResponseModel:         result.UpstreamResponseModel,
			UpstreamResponseModelConflict: result.UpstreamResponseModelConflict,
			InboundEndpoint:               endpoint,
			UpstreamEndpoint:              firstNonEmptyManagedValue(result.UpstreamEndpoint, endpoint),
			InputTokens:                   result.Usage.InputTokens,
			OutputTokens:                  result.Usage.OutputTokens,
			CacheReadTokens:               result.Usage.CacheReadInputTokens,
			APIKey:                        managedBatchAPIKey(executionCtx),
			Account:                       managedBatchAccount(executionCtx, accountID),
		})
		if settleErr != nil {
			_, retryErr := p.repo.RetryItem(persistCtx, item.ID, p.cfg.WorkerID, time.Now().Add(p.cfg.RetryDelay), errorMap("settlement_error", settleErr.Error()))
			return errors.Join(settleErr, retryErr)
		}
	}
	if p.cfg.ResultFinalizer == nil {
		lineResult := batchcontract.ResultLine{ID: strconv.FormatInt(item.ID, 10), CustomID: item.CustomID, Response: &result.Response}
		if err := p.index(persistCtx, payload.BatchID, lineResult); err != nil {
			return p.completeUnknown(persistCtx, payload.BatchID, item, err)
		}
	}
	_, completeErr := p.repo.CompleteItem(persistCtx, item.ID, p.cfg.WorkerID, batchcontract.ItemStatusSucceeded, map[string]any{"status_code": result.Response.StatusCode, "request_id": result.Response.RequestID, "body": json.RawMessage(result.Response.Body)}, nil, int64(result.Usage.InputTokens), int64(result.Usage.OutputTokens), false)
	return completeErr
}

func firstNonEmptyManagedValue(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func managedBatchRequestID(batchID string, itemID int64) string {
	return "managed-batch:" + batchID + ":item:" + strconv.FormatInt(itemID, 10)
}

func managedBatchAPIKey(ctx context.Context) *service.APIKey {
	execution, ok := service.ManagedBatchExecutionFromContext(ctx)
	if !ok {
		return nil
	}
	return execution.APIKey
}

func managedBatchAccount(ctx context.Context, accountID int64) *service.Account {
	execution, ok := service.ManagedBatchExecutionFromContext(ctx)
	if !ok || execution.Selection == nil || execution.Selection.Account == nil || execution.Selection.Account.ID != accountID {
		return nil
	}
	return execution.Selection.Account
}

func (p *Processor) completeUnknown(ctx context.Context, batchID string, item repository.ManagedBatchItem, cause error) error {
	var indexErr error
	if p.cfg.ResultFinalizer == nil {
		line := batchcontract.ResultLine{ID: strconv.FormatInt(item.ID, 10), CustomID: item.CustomID, Error: &batchcontract.BatchError{Code: batchcontract.ErrorUnknownOutcome, Message: cause.Error()}}
		indexErr = p.index(ctx, batchID, line)
	}
	_, completeErr := p.repo.CompleteItem(ctx, item.ID, p.cfg.WorkerID, itemStatusUnknown, nil, errorMap(batchcontract.ErrorUnknownOutcome, cause.Error()), 0, 0, true)
	return errors.Join(indexErr, completeErr)
}

func (p *Processor) index(ctx context.Context, batchID string, line batchcontract.ResultLine) error {
	if p.cfg.ResultIndexer == nil {
		return fmt.Errorf("%w: result indexer is not configured", ErrResultIndex)
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
	if updater, ok := p.repo.(ProgressUpdater); ok {
		if err := updater.UpdateBatchProgress(ctx, payload.BatchID, progress); err != nil {
			return service.ManagedBatchProcessResult{}, err
		}
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
	if p.cfg.ResultFinalizer != nil {
		if b.Status == batchcontract.BatchStatusInProgress || b.Status == batchcontract.BatchStatusCancelling {
			changed, err := p.transition(ctx, payload, b, batchcontract.BatchStatusFinalizing, "", "", nil)
			if err != nil {
				return service.ManagedBatchProcessResult{}, err
			}
			if changed {
				b.Version++
				b.Status = batchcontract.BatchStatusFinalizing
			} else {
				fresh, err := p.repo.GetBatchForOwner(ctx, payload.UserID, payload.APIKeyID, payload.BatchID)
				if err != nil {
					return service.ManagedBatchProcessResult{}, err
				}
				if batchcontract.IsBatchTerminal(fresh.Status) {
					return service.ManagedBatchProcessResult{Terminal: true}, nil
				}
				b = fresh
				if b.Status != batchcontract.BatchStatusFinalizing {
					return service.ManagedBatchProcessResult{RequeueAfter: p.cfg.RetryDelay}, nil
				}
			}
		}
		if err := p.cfg.ResultFinalizer.Finalize(ctx, payload.BatchID); err != nil {
			return service.ManagedBatchProcessResult{}, err
		}
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

//go:build unit

package batchworker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/batchadapter"
	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type fakeRepo struct {
	mu        sync.Mutex
	batch     repository.ManagedBatch
	items     []repository.ManagedBatchItem
	completed map[int64]string
	retried   int
	events    []repository.ManagedBatchOutboxEvent
}

type validatingRaceRepo struct {
	*fakeRepo
	loseCAS bool
}

func (r *validatingRaceRepo) CompareAndSwapBatchStatusWithOutbox(ctx context.Context, userID, apiKeyID int64, id string, version int64, status string, retryAt *time.Time, code, message *string, event *repository.ManagedBatchOutboxEvent) (bool, error) {
	r.mu.Lock()
	if r.loseCAS {
		r.loseCAS = false
		r.batch.Status = batchcontract.BatchStatusInProgress
		r.batch.Version++
		r.mu.Unlock()
		return false, nil
	}
	r.mu.Unlock()
	return r.fakeRepo.CompareAndSwapBatchStatusWithOutbox(ctx, userID, apiKeyID, id, version, status, retryAt, code, message, event)
}

func newFakeRepo(status string, deadline *time.Time) *fakeRepo {
	return &fakeRepo{batch: repository.ManagedBatch{ID: "batch-1", UserID: 7, Endpoint: batchcontract.EndpointResponses, Status: status, Version: 0, DeadlineAt: deadline}, completed: map[int64]string{}}
}
func (r *fakeRepo) GetBatchForOwner(context.Context, int64, int64, string) (*repository.ManagedBatch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.batch
	return &b, nil
}
func (r *fakeRepo) ClaimItems(_ context.Context, batchID, worker string, limit int, _ time.Duration) ([]repository.ManagedBatchItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []repository.ManagedBatchItem{}
	for i := range r.items {
		if r.items[i].BatchID != batchID || r.items[i].Status != batchcontract.ItemStatusPending || len(out) >= limit {
			continue
		}
		r.items[i].Status = batchcontract.ItemStatusRunning
		r.items[i].LeaseOwner = worker
		out = append(out, r.items[i])
	}
	return out, nil
}
func (r *fakeRepo) CompleteItem(_ context.Context, id int64, worker, status string, _ map[string]any, _ map[string]any, _, _ int64, _ bool) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.items {
		if r.items[i].ID == id && r.items[i].LeaseOwner == worker {
			r.items[i].Status = status
			r.items[i].LeaseOwner = ""
			r.completed[id] = status
			return true, nil
		}
	}
	return false, nil
}
func (r *fakeRepo) RetryItem(_ context.Context, id int64, worker string, _ time.Time, _ map[string]any) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.items {
		if r.items[i].ID == id && r.items[i].LeaseOwner == worker {
			r.items[i].Status = "retry"
			r.items[i].LeaseOwner = ""
			r.retried++
			return true, nil
		}
	}
	return false, nil
}
func (r *fakeRepo) CancelPendingItems(_ context.Context, batchID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for i := range r.items {
		if r.items[i].BatchID == batchID && (r.items[i].Status == batchcontract.ItemStatusPending || r.items[i].Status == "retry") {
			r.items[i].Status = batchcontract.ItemStatusCancelled
			count++
		}
	}
	return count, nil
}
func (r *fakeRepo) CountBatchItems(context.Context, string) (repository.ManagedBatchProgress, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var p repository.ManagedBatchProgress
	for _, i := range r.items {
		p.Total++
		switch i.Status {
		case batchcontract.ItemStatusSucceeded:
			p.Succeeded++
		case batchcontract.ItemStatusFailed:
			p.Failed++
		case batchcontract.ItemStatusCancelled:
			p.Cancelled++
		case batchcontract.ItemStatusUnknown:
			p.Unknown++
		case batchcontract.ItemStatusPending:
			p.Pending++
		case batchcontract.ItemStatusRunning:
			p.Running++
		case "retry":
			p.Retry++
		}
	}
	return p, nil
}
func (r *fakeRepo) CompareAndSwapBatchStatusWithOutbox(_ context.Context, _, _ int64, id string, version int64, status string, _ *time.Time, _, _ *string, event *repository.ManagedBatchOutboxEvent) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.batch.ID || version != r.batch.Version {
		return false, nil
	}
	r.batch.Status = status
	r.batch.Version++
	if event != nil {
		r.events = append(r.events, *event)
	}
	return true, nil
}

type fakeRunner struct {
	run func(context.Context) (batchadapter.ProviderResponse, error)
}

func (r fakeRunner) Run(ctx context.Context, _ batchadapter.ProviderRequest) (batchadapter.ProviderResponse, error) {
	return r.run(ctx)
}

type fakeResolver struct {
	key     *service.APIKey
	account *service.Account
}

func (r fakeResolver) Resolve(ctx context.Context, _ batchadapter.ExecutionRequest, b batchadapter.ExecutionBatch, item batchadapter.ExecutionItem, _ batchcontract.RequestLine) (batchadapter.ExecutionSelection, error) {
	return batchadapter.ExecutionSelection{
		Provider:  batchadapter.ProviderOpenAI,
		AccountID: r.account.ID,
		Context: service.WithManagedBatchExecution(ctx, service.ManagedBatchExecution{
			APIKey: r.key,
			Selection: &service.AccountSelectionResult{
				Account: r.account,
			},
		}),
	}, nil
}

type fakeSettlement struct {
	inputs []service.ManagedBatchSettlementInput
	err    error
}

func (s *fakeSettlement) SettleManagedBatch(_ context.Context, in service.ManagedBatchSettlementInput) error {
	s.inputs = append(s.inputs, in)
	return s.err
}

type memoryIndexer struct {
	mu    sync.Mutex
	lines []batchcontract.ResultLine
	err   error
}

type memoryFinalizer struct {
	calls int
	err   error
}

func (f *memoryFinalizer) Finalize(context.Context, string) error {
	f.calls++
	return f.err
}

func (i *memoryIndexer) Index(_ context.Context, _ string, line batchcontract.ResultLine) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.err != nil {
		return i.err
	}
	i.lines = append(i.lines, line)
	return nil
}

func testPayload(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(JobPayload{BatchID: "batch-1", UserID: 7, Provider: batchadapter.ProviderOpenAI, AccountID: 42})
	require.NoError(t, err)
	return b
}
func testItem() repository.ManagedBatchItem {
	return repository.ManagedBatchItem{ID: 11, BatchID: "batch-1", CustomID: "row-1", Status: batchcontract.ItemStatusPending, Request: map[string]any{"custom_id": "row-1", "method": "POST", "url": batchcontract.EndpointResponses, "body": map[string]any{"model": "gpt-5"}}}
}

func TestProcessorDisabledDoesNotConstructWorker(t *testing.T) {
	require.Nil(t, NewProcessor(Config{}, newFakeRepo(batchcontract.BatchStatusValidating, nil), nil))
}

func TestProcessorManagedSuccessReconcilesAndEmitsOutbox(t *testing.T) {
	r := newFakeRepo(batchcontract.BatchStatusValidating, nil)
	r.items = []repository.ManagedBatchItem{testItem()}
	idx := &memoryIndexer{}
	adapter := batchadapter.NewOpenAIAdapter(fakeRunner{run: func(context.Context) (batchadapter.ProviderResponse, error) {
		return batchadapter.ProviderResponse{StatusCode: 200, Body: []byte(`{"id":"resp-1","usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)}, nil
	}})
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1", ResultIndexer: idx}, r, map[string]batchadapter.ManagedExecutor{batchadapter.ProviderOpenAI: adapter})
	result, err := p.Process(context.Background(), service.ManagedBatchJob{ID: "batch-1", Payload: testPayload(t)})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, batchcontract.BatchStatusCompleted, r.batch.Status)
	require.Len(t, r.events, 1)
	require.Len(t, idx.lines, 1)
	require.Equal(t, batchcontract.ItemStatusSucceeded, r.completed[11])
	// A redelivered queue job sees no pending item and does not call the runner
	// again; reconciliation remains idempotent under at-least-once delivery.
	result, err = p.Process(context.Background(), service.ManagedBatchJob{ID: "batch-1", Payload: testPayload(t)})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Len(t, r.events, 1)
}

func TestProcessorReloadsBatchAfterValidatingCASConflict(t *testing.T) {
	base := newFakeRepo(batchcontract.BatchStatusValidating, nil)
	base.items = []repository.ManagedBatchItem{testItem()}
	r := &validatingRaceRepo{fakeRepo: base, loseCAS: true}
	adapter := batchadapter.NewOpenAIAdapter(fakeRunner{run: func(context.Context) (batchadapter.ProviderResponse, error) {
		return batchadapter.ProviderResponse{StatusCode: 200, Body: []byte(`{"id":"resp-1"}`)}, nil
	}})
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1", ResultIndexer: &memoryIndexer{}}, r, map[string]batchadapter.ManagedExecutor{batchadapter.ProviderOpenAI: adapter})
	result, err := p.Process(context.Background(), service.ManagedBatchJob{Payload: testPayload(t)})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, batchcontract.BatchStatusCompleted, r.batch.Status)
	require.Equal(t, batchcontract.ItemStatusSucceeded, r.completed[11])
}

func TestProcessorFinalizesResultsBeforeTerminalTransition(t *testing.T) {
	r := newFakeRepo(batchcontract.BatchStatusInProgress, nil)
	r.items = []repository.ManagedBatchItem{testItem()}
	idx := &memoryIndexer{}
	finalizer := &memoryFinalizer{}
	adapter := batchadapter.NewOpenAIAdapter(fakeRunner{run: func(context.Context) (batchadapter.ProviderResponse, error) {
		return batchadapter.ProviderResponse{StatusCode: 200, Body: []byte(`{"id":"resp-1"}`)}, nil
	}})
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1", ResultIndexer: idx, ResultFinalizer: finalizer}, r, map[string]batchadapter.ManagedExecutor{batchadapter.ProviderOpenAI: adapter})
	result, err := p.Process(context.Background(), service.ManagedBatchJob{ID: "batch-1", Payload: testPayload(t)})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, 1, finalizer.calls)
	require.Equal(t, batchcontract.BatchStatusCompleted, r.batch.Status)
}

func TestProcessorFinalizerDoesNotRequireLegacyResultIndexer(t *testing.T) {
	r := newFakeRepo(batchcontract.BatchStatusInProgress, nil)
	r.items = []repository.ManagedBatchItem{testItem()}
	finalizer := &memoryFinalizer{}
	adapter := batchadapter.NewOpenAIAdapter(fakeRunner{run: func(context.Context) (batchadapter.ProviderResponse, error) {
		return batchadapter.ProviderResponse{StatusCode: 200, Body: []byte(`{"id":"resp-1"}`)}, nil
	}})
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1", ResultFinalizer: finalizer}, r, map[string]batchadapter.ManagedExecutor{batchadapter.ProviderOpenAI: adapter})
	result, err := p.Process(context.Background(), service.ManagedBatchJob{ID: "batch-1", Payload: testPayload(t)})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, batchcontract.BatchStatusCompleted, r.batch.Status)
	require.Equal(t, batchcontract.ItemStatusSucceeded, r.completed[11])
}

func TestProcessorSettlesSuccessfulItemWithStableItemKey(t *testing.T) {
	r := newFakeRepo(batchcontract.BatchStatusInProgress, nil)
	r.items = []repository.ManagedBatchItem{testItem()}
	settlement := &fakeSettlement{}
	key := &service.APIKey{ID: 8, UserID: 7}
	account := &service.Account{ID: 42}
	adapter := batchadapter.NewOpenAIAdapter(fakeRunner{run: func(context.Context) (batchadapter.ProviderResponse, error) {
		return batchadapter.ProviderResponse{StatusCode: 200, Body: []byte(`{"id":"resp-1","usage":{"input_tokens":3,"output_tokens":2,"input_tokens_details":{"cached_tokens":1}}}`)}, nil
	}})
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1", ResultIndexer: &memoryIndexer{}, Settlement: settlement}, r, map[string]batchadapter.ManagedExecutor{batchadapter.ProviderOpenAI: adapter})
	p.cfg.Resolver = fakeResolver{key: key, account: account}
	result, err := p.Process(context.Background(), service.ManagedBatchJob{Payload: testPayload(t)})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Len(t, settlement.inputs, 1)
	require.Equal(t, "managed-batch:batch-1:item:11", settlement.inputs[0].RequestID)
	require.Equal(t, 3, settlement.inputs[0].InputTokens)
	require.Equal(t, 2, settlement.inputs[0].OutputTokens)
	require.Equal(t, 1, settlement.inputs[0].CacheReadTokens)
	require.Same(t, key, settlement.inputs[0].APIKey)
	require.Same(t, account, settlement.inputs[0].Account)
}

func TestProcessorSettlementFailureRetriesWithoutTerminalSuccess(t *testing.T) {
	r := newFakeRepo(batchcontract.BatchStatusInProgress, nil)
	r.items = []repository.ManagedBatchItem{testItem()}
	settlement := &fakeSettlement{err: errors.New("billing temporarily unavailable")}
	key := &service.APIKey{ID: 8, UserID: 7}
	account := &service.Account{ID: 42}
	adapter := batchadapter.NewOpenAIAdapter(fakeRunner{run: func(context.Context) (batchadapter.ProviderResponse, error) {
		return batchadapter.ProviderResponse{StatusCode: 200, Body: []byte(`{"usage":{"input_tokens":3,"output_tokens":2}}`)}, nil
	}})
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1", ResultIndexer: &memoryIndexer{}, Settlement: settlement}, r, map[string]batchadapter.ManagedExecutor{batchadapter.ProviderOpenAI: adapter})
	p.cfg.Resolver = fakeResolver{key: key, account: account}
	result, err := p.Process(context.Background(), service.ManagedBatchJob{Payload: testPayload(t)})
	require.Error(t, err)
	require.False(t, result.Terminal)
	require.Equal(t, 1, r.retried)
	require.Empty(t, r.completed)
}

func TestProcessorCancellingBatchCancelsUnstartedItems(t *testing.T) {
	r := newFakeRepo(batchcontract.BatchStatusCancelling, nil)
	r.items = []repository.ManagedBatchItem{testItem()}
	finalizer := &memoryFinalizer{}
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1", ResultFinalizer: finalizer}, r, nil)
	result, err := p.Process(context.Background(), service.ManagedBatchJob{ID: "batch-1", Payload: testPayload(t)})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, batchcontract.BatchStatusCancelled, r.batch.Status)
	require.Equal(t, batchcontract.ItemStatusCancelled, r.items[0].Status)
	require.Equal(t, 1, finalizer.calls)
}

func TestProcessorRetryableProviderErrorLeavesItemRetryable(t *testing.T) {
	r := newFakeRepo(batchcontract.BatchStatusInProgress, nil)
	r.items = []repository.ManagedBatchItem{testItem()}
	adapter := batchadapter.NewOpenAIAdapter(fakeRunner{run: func(context.Context) (batchadapter.ProviderResponse, error) {
		return batchadapter.ProviderResponse{StatusCode: 429, Body: []byte(`{"error":{"code":"rate_limit","message":"slow down"}}`)}, nil
	}})
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1"}, r, map[string]batchadapter.ManagedExecutor{batchadapter.ProviderOpenAI: adapter})
	result, err := p.Process(context.Background(), service.ManagedBatchJob{Payload: testPayload(t)})
	require.NoError(t, err)
	require.False(t, result.Terminal)
	require.Equal(t, 1, r.retried)
	require.Empty(t, r.events)
}

func TestProcessorNilRunnerFailsClosedAsPermanentItemFailure(t *testing.T) {
	r := newFakeRepo(batchcontract.BatchStatusInProgress, nil)
	r.items = []repository.ManagedBatchItem{testItem()}
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1"}, r, map[string]batchadapter.ManagedExecutor{batchadapter.ProviderOpenAI: batchadapter.NewOpenAIAdapter(nil)})
	result, err := p.Process(context.Background(), service.ManagedBatchJob{Payload: testPayload(t)})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, batchcontract.ItemStatusFailed, r.completed[11])
}

func TestProcessorCancellationAfterProviderStartMarksUnknown(t *testing.T) {
	r := newFakeRepo(batchcontract.BatchStatusInProgress, nil)
	r.items = []repository.ManagedBatchItem{testItem()}
	started := make(chan struct{})
	adapter := batchadapter.NewOpenAIAdapter(fakeRunner{run: func(ctx context.Context) (batchadapter.ProviderResponse, error) {
		close(started)
		<-ctx.Done()
		return batchadapter.ProviderResponse{}, ctx.Err()
	}})
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1", ResultIndexer: &memoryIndexer{}}, r, map[string]batchadapter.ManagedExecutor{batchadapter.ProviderOpenAI: adapter})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	result, err := p.Process(ctx, service.ManagedBatchJob{Payload: testPayload(t)})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, itemStatusUnknown, r.completed[11])
}

func TestProcessorDeadlineExpiresBeforeClaim(t *testing.T) {
	deadline := time.Now().Add(-time.Second)
	r := newFakeRepo(batchcontract.BatchStatusInProgress, &deadline)
	r.items = []repository.ManagedBatchItem{testItem()}
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1"}, r, nil)
	result, err := p.Process(context.Background(), service.ManagedBatchJob{Payload: testPayload(t)})
	require.NoError(t, err)
	require.True(t, result.Terminal)
	require.Equal(t, batchcontract.BatchStatusExpired, r.batch.Status)
	require.Empty(t, r.completed)
}

func TestProcessorResultIndexerFailureDoesNotClaimSuccess(t *testing.T) {
	r := newFakeRepo(batchcontract.BatchStatusInProgress, nil)
	r.items = []repository.ManagedBatchItem{testItem()}
	idx := &memoryIndexer{err: errors.New("disk unavailable")}
	adapter := batchadapter.NewOpenAIAdapter(fakeRunner{run: func(context.Context) (batchadapter.ProviderResponse, error) {
		return batchadapter.ProviderResponse{StatusCode: 200, Body: []byte(`{"id":"resp"}`)}, nil
	}})
	p := NewProcessor(Config{Enabled: true, WorkerID: "w1", ResultIndexer: idx}, r, map[string]batchadapter.ManagedExecutor{batchadapter.ProviderOpenAI: adapter})
	_, err := p.Process(context.Background(), service.ManagedBatchJob{Payload: testPayload(t)})
	require.ErrorIs(t, err, ErrResultIndex)
	require.Equal(t, itemStatusUnknown, r.completed[11])
}

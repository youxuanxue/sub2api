package service

import (
	"context"
	"errors"
	"time"
)

// ManagedBatchJob is the queue payload. Payload is an opaque, serialized
// request owned by the batch contract/provider adapter.
type ManagedBatchJob struct {
	ID      string
	Shard   string
	Payload []byte
}

type ManagedBatchDelivery struct {
	Job        ManagedBatchJob
	ReservedAt time.Time
}

var (
	ErrManagedBatchQueueEmpty    = errors.New("managed batch queue empty")
	ErrManagedBatchAlreadyQueued = errors.New("managed batch job already queued")
	ErrManagedBatchInvalidJob    = errors.New("invalid managed batch job")
	ErrManagedBatchLeaseConflict = errors.New("managed batch lease conflict")
	ErrManagedBatchLeaseLost     = errors.New("managed batch lease lost")
	ErrManagedBatchOutboxStalled = errors.New("managed batch outbox stalled")
)

// ManagedBatchLease is a fencing lease. Implementations must only refresh or
// release a lease while its owner token still matches.
type ManagedBatchLease interface {
	Refresh(context.Context, time.Duration) error
	Release(context.Context) error
}

// ManagedBatchQueue is deliberately narrower than the public Batch API. It
// owns ready/active/delayed queues, job leases and stale recovery.
type ManagedBatchQueue interface {
	Enqueue(context.Context, ManagedBatchJob) error
	Reserve(context.Context, time.Duration) (ManagedBatchDelivery, error)
	Ack(context.Context, string) error
	Requeue(context.Context, string, time.Duration) error
	Heartbeat(context.Context, string) error
	RecoverStale(context.Context, time.Duration, int) (int, error)
	TryAcquireJobLease(context.Context, string, time.Duration) (ManagedBatchLease, bool, error)
}

// ManagedBatchProcessor executes one reserved job. Terminal=true acknowledges
// the delivery; otherwise the worker requeues it using RequeueAfter.
type ManagedBatchProcessor interface {
	Process(context.Context, ManagedBatchJob) (ManagedBatchProcessResult, error)
}

type ManagedBatchProcessResult struct {
	Terminal     bool
	RequeueAfter time.Duration
}

type ManagedBatchWorkerOptions struct {
	ReserveBlockTimeout time.Duration
	JobLeaseTTL         time.Duration
	HeartbeatInterval   time.Duration
	RetryDelay          time.Duration
	RecoveryInterval    time.Duration
	StaleAfter          time.Duration
	RecoverLimit        int
}

func (o ManagedBatchWorkerOptions) withDefaults() ManagedBatchWorkerOptions {
	if o.ReserveBlockTimeout <= 0 {
		o.ReserveBlockTimeout = 5 * time.Second
	}
	if o.JobLeaseTTL <= 0 {
		o.JobLeaseTTL = 5 * time.Minute
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = o.JobLeaseTTL / 3
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = time.Second
	}
	if o.RetryDelay <= 0 {
		o.RetryDelay = time.Minute
	}
	if o.RecoveryInterval <= 0 {
		o.RecoveryInterval = time.Minute
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 10 * time.Minute
	}
	if o.RecoverLimit <= 0 {
		o.RecoverLimit = 100
	}
	return o
}

// ManagedBatchWorker is intentionally a shell: provider selection, protocol
// conversion, persistence and billing stay behind ManagedBatchProcessor.
type ManagedBatchWorker struct {
	Queue     ManagedBatchQueue
	Processor ManagedBatchProcessor
	Options   ManagedBatchWorkerOptions
}

func NewManagedBatchWorker(queue ManagedBatchQueue, processor ManagedBatchProcessor, opts ManagedBatchWorkerOptions) *ManagedBatchWorker {
	return &ManagedBatchWorker{Queue: queue, Processor: processor, Options: opts.withDefaults()}
}

func (w *ManagedBatchWorker) RunOnce(ctx context.Context) error {
	if w == nil || w.Queue == nil || w.Processor == nil {
		return nil
	}
	delivery, err := w.Queue.Reserve(ctx, w.Options.ReserveBlockTimeout)
	if err != nil {
		return err
	}
	lease, acquired, err := w.Queue.TryAcquireJobLease(ctx, delivery.Job.ID, w.Options.JobLeaseTTL)
	if err != nil {
		_ = w.Queue.Requeue(ctx, delivery.Job.ID, w.Options.RetryDelay)
		return err
	}
	if !acquired {
		return w.Queue.Requeue(ctx, delivery.Job.ID, w.Options.RetryDelay)
	}
	defer func() { _ = lease.Release(context.Background()) }()

	stop := make(chan struct{})
	done := make(chan struct{})
	go w.heartbeat(ctx, delivery.Job.ID, lease, stop, done)
	result, processErr := w.Processor.Process(ctx, delivery.Job)
	close(stop)
	<-done
	if processErr != nil {
		return w.Queue.Requeue(ctx, delivery.Job.ID, w.Options.RetryDelay)
	}
	if result.Terminal {
		return w.Queue.Ack(ctx, delivery.Job.ID)
	}
	delay := result.RequeueAfter
	if delay <= 0 {
		delay = w.Options.RetryDelay
	}
	return w.Queue.Requeue(ctx, delivery.Job.ID, delay)
}

func (w *ManagedBatchWorker) heartbeat(ctx context.Context, id string, lease ManagedBatchLease, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(w.Options.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			_ = w.Queue.Heartbeat(ctx, id)
			_ = lease.Refresh(ctx, w.Options.JobLeaseTTL)
		}
	}
}

func (w *ManagedBatchWorker) RecoverStaleOnce(ctx context.Context) (int, error) {
	if w == nil || w.Queue == nil {
		return 0, nil
	}
	return w.Queue.RecoverStale(ctx, w.Options.StaleAfter, w.Options.RecoverLimit)
}

// ManagedBatchOutboxEvent is the durable handoff from DB transaction to Redis.
type ManagedBatchOutboxEvent struct {
	ID  int64
	Job ManagedBatchJob
}

type ManagedBatchOutbox interface {
	ListAfter(context.Context, int64, int) ([]ManagedBatchOutboxEvent, error)
}

type ManagedBatchOutboxCursor interface {
	Load(context.Context) (int64, error)
	Store(context.Context, int64) error
}

// ManagedBatchOutboxPublisher is replay-safe: it advances the cursor only
// after enqueue succeeds. A crash before Store causes duplicate delivery;
// queue idempotency makes that harmless.
type ManagedBatchOutboxPublisher struct {
	Outbox    ManagedBatchOutbox
	Cursor    ManagedBatchOutboxCursor
	Queue     ManagedBatchQueue
	BatchSize int
}

func (p *ManagedBatchOutboxPublisher) PublishOnce(ctx context.Context) (int, error) {
	if p == nil || p.Outbox == nil || p.Cursor == nil || p.Queue == nil {
		return 0, nil
	}
	limit := p.BatchSize
	if limit <= 0 {
		limit = 100
	}
	after, err := p.Cursor.Load(ctx)
	if err != nil {
		return 0, err
	}
	events, err := p.Outbox.ListAfter(ctx, after, limit)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, event := range events {
		if err := p.Queue.Enqueue(ctx, event.Job); err != nil && !errors.Is(err, ErrManagedBatchAlreadyQueued) {
			return count, err
		}
		if err := p.Cursor.Store(ctx, event.ID); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

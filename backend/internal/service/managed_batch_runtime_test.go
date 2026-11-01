//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type managedMemoryQueue struct {
	mu     sync.Mutex
	jobs   []ManagedBatchJob
	active map[string]bool
	acked  map[string]bool
}

type contextCheckingQueue struct{ *managedMemoryQueue }

func (q *contextCheckingQueue) Requeue(ctx context.Context, id string, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return q.managedMemoryQueue.Requeue(ctx, id, delay)
}

func newManagedMemoryQueue() *managedMemoryQueue {
	return &managedMemoryQueue{active: map[string]bool{}, acked: map[string]bool{}}
}
func (q *managedMemoryQueue) Enqueue(_ context.Context, j ManagedBatchJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, x := range q.jobs {
		if x.ID == j.ID {
			return ErrManagedBatchAlreadyQueued
		}
	}
	q.jobs = append(q.jobs, j)
	return nil
}
func (q *managedMemoryQueue) Reserve(_ context.Context, _ time.Duration) (ManagedBatchDelivery, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) == 0 {
		return ManagedBatchDelivery{}, ErrManagedBatchQueueEmpty
	}
	j := q.jobs[0]
	q.jobs = q.jobs[1:]
	q.active[j.ID] = true
	return ManagedBatchDelivery{Job: j}, nil
}
func (q *managedMemoryQueue) Ack(_ context.Context, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active[id] = false
	q.acked[id] = true
	return nil
}
func (q *managedMemoryQueue) Requeue(_ context.Context, id string, _ time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active[id] = false
	q.jobs = append(q.jobs, ManagedBatchJob{ID: id})
	return nil
}
func (q *managedMemoryQueue) Heartbeat(context.Context, string) error { return nil }
func (q *managedMemoryQueue) RecoverStale(context.Context, time.Duration, int) (int, error) {
	return 0, nil
}
func (q *managedMemoryQueue) TryAcquireJobLease(context.Context, string, time.Duration) (ManagedBatchLease, bool, error) {
	return managedNoopLease{}, true, nil
}

type managedNoopLease struct{}

func (managedNoopLease) Refresh(context.Context, time.Duration) error { return nil }
func (managedNoopLease) Release(context.Context) error                { return nil }

type managedProcessor struct {
	calls    int
	terminal bool
}

type cancellingManagedProcessor struct{ cancel context.CancelFunc }

func (p *cancellingManagedProcessor) Process(context.Context, ManagedBatchJob) (ManagedBatchProcessResult, error) {
	p.cancel()
	return ManagedBatchProcessResult{}, context.Canceled
}

func (p *managedProcessor) Process(_ context.Context, _ ManagedBatchJob) (ManagedBatchProcessResult, error) {
	p.calls++
	return ManagedBatchProcessResult{Terminal: p.terminal}, nil
}

func TestManagedBatchWorker_RunOnceAcknowledgesTerminalJob(t *testing.T) {
	q := newManagedMemoryQueue()
	p := &managedProcessor{terminal: true}
	require.NoError(t, q.Enqueue(context.Background(), ManagedBatchJob{ID: "job", Payload: []byte("x")}))
	w := NewManagedBatchWorker(q, p, ManagedBatchWorkerOptions{ReserveBlockTimeout: time.Millisecond})
	require.NoError(t, w.RunOnce(context.Background()))
	require.Equal(t, 1, p.calls)
	require.True(t, q.acked["job"])
}

func TestManagedBatchWorker_RequeuesAfterProcessorCancellation(t *testing.T) {
	q := &contextCheckingQueue{managedMemoryQueue: newManagedMemoryQueue()}
	require.NoError(t, q.Enqueue(context.Background(), ManagedBatchJob{ID: "job", Payload: []byte("x")}))
	ctx, cancel := context.WithCancel(context.Background())
	p := &cancellingManagedProcessor{cancel: cancel}
	w := NewManagedBatchWorker(q, p, ManagedBatchWorkerOptions{ReserveBlockTimeout: time.Millisecond})
	require.NoError(t, w.RunOnce(ctx))
	require.Len(t, q.jobs, 1)
}

type managedMemoryOutbox struct{ events []ManagedBatchOutboxEvent }

func (o *managedMemoryOutbox) ListAfter(_ context.Context, after int64, limit int) ([]ManagedBatchOutboxEvent, error) {
	out := []ManagedBatchOutboxEvent{}
	for _, e := range o.events {
		if e.ID > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

type managedMemoryCursor struct{ id int64 }

func (c *managedMemoryCursor) Load(context.Context) (int64, error)     { return c.id, nil }
func (c *managedMemoryCursor) Store(_ context.Context, id int64) error { c.id = id; return nil }

func TestManagedBatchOutboxPublisher_ReplaysAfterCursorAndDeduplicates(t *testing.T) {
	q := newManagedMemoryQueue()
	o := &managedMemoryOutbox{events: []ManagedBatchOutboxEvent{{ID: 1, Job: ManagedBatchJob{ID: "a"}}, {ID: 2, Job: ManagedBatchJob{ID: "a"}}}}
	c := &managedMemoryCursor{}
	p := &ManagedBatchOutboxPublisher{Outbox: o, Cursor: c, Queue: q, BatchSize: 10}
	n, err := p.PublishOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, int64(2), c.id)
	require.Len(t, q.jobs, 1)
}

func TestManagedBatchRuntimeDisabledDoesNotStartLoops(t *testing.T) {
	runtime := NewManagedBatchRuntime(nil, nil, nil, false)
	runtime.Start()
	require.False(t, runtime.Running())
	runtime.Stop()
}

package service

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const defaultTerminalOutcomeQueueCapacity = 8192

type TerminalOutcomeKind string

const (
	TerminalOutcomeSuccess           TerminalOutcomeKind = "success"
	TerminalOutcomeFinalEmptyPool429 TerminalOutcomeKind = "final_empty_pool_429"
	TerminalOutcomeOtherError        TerminalOutcomeKind = "other_error"
)

type TerminalOutcomeEvent struct {
	At             time.Time
	GroupID        int64
	RequestedModel string
	Kind           TerminalOutcomeKind
}

type TerminalOutcomeFact struct {
	BucketStart       time.Time
	GroupID           int64
	RequestedModel    string
	ProducerEpoch     string
	SuccessCount      int64
	EmptyPool429Count int64
	OtherErrorCount   int64
}

type TerminalOutcomeHealth struct {
	BucketStart       time.Time
	ProducerEpoch     string
	SeenCount         int64
	PersistedCount    int64
	DropCount         int64
	FlushFailureCount int64
	Complete          bool
}

type TerminalOutcomeMinuteFlush struct {
	Facts  []TerminalOutcomeFact
	Health TerminalOutcomeHealth
}

type TerminalOutcomeRepository interface {
	FlushMinute(context.Context, TerminalOutcomeMinuteFlush) error
}

type terminalOutcomeFactKey struct {
	groupID int64
	model   string
}

type terminalOutcomeCounts struct {
	success   int64
	emptyPool int64
	other     int64
}

type terminalOutcomeMinute struct {
	seen          int64
	dropped       int64
	flushFailures int64
	facts         map[terminalOutcomeFactKey]terminalOutcomeCounts
}

type TerminalOutcomeRecorder struct {
	repo  TerminalOutcomeRepository
	queue chan TerminalOutcomeEvent
	now   func() time.Time
	epoch string

	mu              sync.Mutex
	minutes         map[time.Time]*terminalOutcomeMinute
	nextHeartbeatAt time.Time

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	done      chan struct{}
}

func NewTerminalOutcomeRecorder(repo TerminalOutcomeRepository) *TerminalOutcomeRecorder {
	return newTerminalOutcomeRecorder(repo, defaultTerminalOutcomeQueueCapacity, time.Now, uuid.NewString())
}

func ProvideTerminalOutcomeRecorder(repo TerminalOutcomeRepository) *TerminalOutcomeRecorder {
	recorder := NewTerminalOutcomeRecorder(repo)
	recorder.Start()
	return recorder
}

func newTerminalOutcomeRecorder(repo TerminalOutcomeRepository, capacity int, now func() time.Time, epoch string) *TerminalOutcomeRecorder {
	if capacity < 1 {
		capacity = 1
	}
	startedAt := now().UTC().Truncate(time.Minute)
	return &TerminalOutcomeRecorder{
		repo:            repo,
		queue:           make(chan TerminalOutcomeEvent, capacity),
		now:             now,
		epoch:           epoch,
		minutes:         make(map[time.Time]*terminalOutcomeMinute),
		nextHeartbeatAt: startedAt,
		done:            make(chan struct{}),
	}
}

func (r *TerminalOutcomeRecorder) Record(event TerminalOutcomeEvent) bool {
	if r == nil || r.repo == nil {
		return false
	}
	event.RequestedModel = strings.TrimSpace(event.RequestedModel)
	if event.RequestedModel == "" || !validTerminalOutcomeKind(event.Kind) {
		return false
	}
	if event.At.IsZero() {
		event.At = r.now()
	}
	event.At = event.At.UTC()
	bucket := event.At.Truncate(time.Minute)

	r.mu.Lock()
	minute := r.minuteLocked(bucket)
	minute.seen++
	select {
	case r.queue <- event:
		r.mu.Unlock()
		return true
	default:
		minute.dropped++
		r.mu.Unlock()
		return false
	}
}

func (r *TerminalOutcomeRecorder) Start() {
	if r == nil || r.repo == nil {
		return
	}
	r.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		r.cancel = cancel
		go r.run(ctx)
	})
}

func (r *TerminalOutcomeRecorder) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		if r.cancel == nil {
			return
		}
		r.cancel()
		<-r.done
	})
}

func (r *TerminalOutcomeRecorder) run(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case event := <-r.queue:
			r.aggregate(event)
		case <-ticker.C:
			_ = r.flushReadyMinutes(ctx)
		case <-ctx.Done():
			r.drainQueue()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = r.flushReadyMinutes(shutdownCtx)
			cancel()
			return
		}
	}
}

func (r *TerminalOutcomeRecorder) flushReadyMinutes(ctx context.Context) error {
	r.drainQueue()
	closeBefore := r.now().UTC().Truncate(time.Minute).Add(-time.Minute)
	for {
		r.mu.Lock()
		bucket := r.nextHeartbeatAt
		if !bucket.Before(closeBefore) {
			r.mu.Unlock()
			return nil
		}
		minute := r.minuteLocked(bucket)
		flush := r.snapshotLocked(bucket, minute)
		r.mu.Unlock()

		if err := r.repo.FlushMinute(ctx, flush); err != nil {
			r.mu.Lock()
			minute.flushFailures++
			r.mu.Unlock()
			return err
		}

		r.mu.Lock()
		delete(r.minutes, bucket)
		r.nextHeartbeatAt = bucket.Add(time.Minute)
		r.mu.Unlock()
	}
}

func (r *TerminalOutcomeRecorder) drainQueue() {
	for {
		select {
		case event := <-r.queue:
			r.aggregate(event)
		default:
			return
		}
	}
}

func (r *TerminalOutcomeRecorder) aggregate(event TerminalOutcomeEvent) {
	bucket := event.At.UTC().Truncate(time.Minute)
	key := terminalOutcomeFactKey{groupID: event.GroupID, model: event.RequestedModel}
	r.mu.Lock()
	defer r.mu.Unlock()
	minute := r.minuteLocked(bucket)
	counts := minute.facts[key]
	switch event.Kind {
	case TerminalOutcomeSuccess:
		counts.success++
	case TerminalOutcomeFinalEmptyPool429:
		counts.emptyPool++
	case TerminalOutcomeOtherError:
		counts.other++
	}
	minute.facts[key] = counts
}

func (r *TerminalOutcomeRecorder) minuteLocked(bucket time.Time) *terminalOutcomeMinute {
	minute := r.minutes[bucket]
	if minute == nil {
		minute = &terminalOutcomeMinute{facts: make(map[terminalOutcomeFactKey]terminalOutcomeCounts)}
		r.minutes[bucket] = minute
	}
	return minute
}

func (r *TerminalOutcomeRecorder) snapshotLocked(bucket time.Time, minute *terminalOutcomeMinute) TerminalOutcomeMinuteFlush {
	facts := make([]TerminalOutcomeFact, 0, len(minute.facts))
	var persisted int64
	for key, counts := range minute.facts {
		persisted += counts.success + counts.emptyPool + counts.other
		facts = append(facts, TerminalOutcomeFact{
			BucketStart:       bucket,
			GroupID:           key.groupID,
			RequestedModel:    key.model,
			ProducerEpoch:     r.epoch,
			SuccessCount:      counts.success,
			EmptyPool429Count: counts.emptyPool,
			OtherErrorCount:   counts.other,
		})
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].GroupID != facts[j].GroupID {
			return facts[i].GroupID < facts[j].GroupID
		}
		return facts[i].RequestedModel < facts[j].RequestedModel
	})
	return TerminalOutcomeMinuteFlush{
		Facts: facts,
		Health: TerminalOutcomeHealth{
			BucketStart:       bucket,
			ProducerEpoch:     r.epoch,
			SeenCount:         minute.seen,
			PersistedCount:    persisted,
			DropCount:         minute.dropped,
			FlushFailureCount: minute.flushFailures,
			Complete:          minute.seen == persisted && minute.dropped == 0 && minute.flushFailures == 0,
		},
	}
}

func validTerminalOutcomeKind(kind TerminalOutcomeKind) bool {
	return kind == TerminalOutcomeSuccess || kind == TerminalOutcomeFinalEmptyPool429 || kind == TerminalOutcomeOtherError
}

package telemetryarchive

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	SchemaVersion = 1

	DefaultQueueSize     = 8192
	DefaultQueueMaxBytes = int64(32 * 1024 * 1024)
	DefaultMaxEventBytes = 1024 * 1024
	DefaultBatchSize     = 256
	DefaultWorkerCount   = 4
	DefaultFlushInterval = 5 * time.Second
	DefaultPutTimeout    = 10 * time.Second
)

type Dataset string

const (
	DatasetUsage     Dataset = "usage"
	DatasetOpsError  Dataset = "ops-error"
	DatasetOpsSystem Dataset = "ops-system"
)

type Config struct {
	Enabled       bool
	Bucket        string
	Prefix        string
	QueueSize     int
	QueueMaxBytes int64
	MaxEventBytes int
	BatchSize     int
	WorkerCount   int
	FlushInterval time.Duration
	PutTimeout    time.Duration
}

type PutRequest struct {
	Bucket          string
	Key             string
	Body            []byte
	ContentType     string
	ContentEncoding string
	Metadata        map[string]string
}

type Uploader interface {
	PutObject(ctx context.Context, request PutRequest) error
}

type Sink interface {
	Enqueue(dataset Dataset, value any) bool
}

type event struct {
	dataset       Dataset
	payload       json.RawMessage
	enqueuedAt    time.Time
	reservedBytes int64
}

type archiveRecord struct {
	SchemaVersion int             `json:"schema_version"`
	Dataset       Dataset         `json:"dataset"`
	EnqueuedAt    time.Time       `json:"enqueued_at"`
	Payload       json.RawMessage `json:"payload"`
}

type Stats struct {
	InstanceID    string    `json:"instance_id"`
	StartedAt     time.Time `json:"started_at"`
	Enqueued      uint64    `json:"enqueued"`
	Dropped       uint64    `json:"dropped"`
	Uploaded      uint64    `json:"uploaded"`
	Failed        uint64    `json:"failed"`
	Pending       int64     `json:"pending"`
	PendingBytes  int64     `json:"pending_bytes"`
	LastUploadAt  time.Time `json:"last_upload_at,omitempty"`
	LastFailureAt time.Time `json:"last_failure_at,omitempty"`
}

// Shadow is a bounded, best-effort raw telemetry writer. Enqueue never waits
// for S3; both record count and serialized bytes bound all queued/in-flight data.
type Shadow struct {
	config    Config
	uploader  Uploader
	queue     chan event
	slots     chan struct{}
	doneCh    chan struct{}
	stopOnce  sync.Once
	stopped   atomic.Bool
	lifecycle sync.RWMutex
	workers   sync.WaitGroup
	sequence  atomic.Uint64
	instance  string
	startedAt time.Time
	enqueued  atomic.Uint64
	dropped   atomic.Uint64
	uploaded  atomic.Uint64
	failed    atomic.Uint64
	pending   atomic.Int64
	bytes     atomic.Int64
	lastPut   atomic.Int64
	lastFail  atomic.Int64
}

func New(config Config, uploader Uploader) *Shadow {
	config = withDefaults(config)
	shadow := &Shadow{
		config:    config,
		uploader:  uploader,
		queue:     make(chan event, config.QueueSize),
		slots:     make(chan struct{}, config.QueueSize),
		doneCh:    make(chan struct{}),
		instance:  uuid.NewString(),
		startedAt: time.Now().UTC(),
	}
	if !shadow.active() {
		close(shadow.doneCh)
		return shadow
	}
	shadow.workers.Add(config.WorkerCount)
	for range config.WorkerCount {
		go shadow.runWorker()
	}
	go func() {
		shadow.workers.Wait()
		close(shadow.doneCh)
	}()
	return shadow
}

func withDefaults(config Config) Config {
	config.Prefix = strings.Trim(strings.TrimSpace(config.Prefix), "/")
	if config.QueueSize <= 0 {
		config.QueueSize = DefaultQueueSize
	}
	if config.QueueMaxBytes <= 0 {
		config.QueueMaxBytes = DefaultQueueMaxBytes
	}
	if config.MaxEventBytes <= 0 {
		config.MaxEventBytes = DefaultMaxEventBytes
	}
	if config.BatchSize <= 0 {
		config.BatchSize = DefaultBatchSize
	}
	if config.BatchSize > config.QueueSize {
		config.BatchSize = config.QueueSize
	}
	if config.WorkerCount <= 0 {
		config.WorkerCount = DefaultWorkerCount
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = DefaultFlushInterval
	}
	if config.PutTimeout <= 0 {
		config.PutTimeout = DefaultPutTimeout
	}
	return config
}

func (s *Shadow) active() bool {
	return s != nil && s.config.Enabled && s.uploader != nil && s.config.Bucket != "" && s.config.Prefix != ""
}

func (s *Shadow) Enabled() bool {
	return s.active()
}

func (s *Shadow) Enqueue(dataset Dataset, value any) bool {
	if !s.active() || s.stopped.Load() || !validDataset(dataset) {
		return false
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.recordDrop("queue_full")
		return false
	}
	maxReservation := int64(s.config.MaxEventBytes)
	if !s.reserveBytes(maxReservation) {
		s.releaseSlot()
		s.recordDrop("queue_bytes_full")
		return false
	}

	payload, err := json.Marshal(value)
	if err != nil {
		s.releaseReservation(maxReservation)
		s.recordDrop("json_marshal")
		return false
	}
	if len(payload) > s.config.MaxEventBytes {
		s.releaseReservation(maxReservation)
		s.recordDrop("event_too_large")
		return false
	}
	reserved := int64(len(payload))
	if refund := maxReservation - reserved; refund > 0 {
		s.bytes.Add(-refund)
	}

	s.lifecycle.RLock()
	defer s.lifecycle.RUnlock()
	if s.stopped.Load() {
		s.releaseReservation(reserved)
		return false
	}
	s.pending.Add(1)
	item := event{
		dataset:       dataset,
		payload:       payload,
		enqueuedAt:    time.Now().UTC(),
		reservedBytes: reserved,
	}
	select {
	case s.queue <- item:
		s.enqueued.Add(1)
		return true
	default:
		s.pending.Add(-1)
		s.releaseReservation(reserved)
		s.recordDrop("queue_full")
		return false
	}
}

func (s *Shadow) reserveBytes(size int64) bool {
	for {
		current := s.bytes.Load()
		if size > s.config.QueueMaxBytes-current {
			return false
		}
		if s.bytes.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

func (s *Shadow) releaseSlot() {
	<-s.slots
}

func (s *Shadow) releaseReservation(size int64) {
	s.bytes.Add(-size)
	s.releaseSlot()
}

func (s *Shadow) releaseEvent(item event) {
	s.pending.Add(-1)
	s.releaseReservation(item.reservedBytes)
}

func (s *Shadow) recordDrop(reason string) {
	dropped := s.dropped.Add(1)
	if dropped == 1 || dropped%1024 == 0 {
		slog.Warn("telemetry archive shadow dropped", "reason", reason, "dropped", dropped)
	}
}

func validDataset(dataset Dataset) bool {
	switch dataset {
	case DatasetUsage, DatasetOpsError, DatasetOpsSystem:
		return true
	default:
		return false
	}
}

func (s *Shadow) runWorker() {
	defer s.workers.Done()
	ticker := time.NewTicker(s.config.FlushInterval)
	defer ticker.Stop()
	batch := make([]event, 0, s.config.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.flush(batch)
		for _, item := range batch {
			s.releaseEvent(item)
		}
		batch = batch[:0]
	}
	for {
		select {
		case item, ok := <-s.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, item)
			if len(batch) >= s.config.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

type batchKey struct {
	dataset Dataset
	date    string
}

func (s *Shadow) flush(batch []event) {
	grouped := make(map[batchKey][]event, 3)
	for _, item := range batch {
		key := batchKey{dataset: item.dataset, date: item.enqueuedAt.UTC().Format("2006-01-02")}
		grouped[key] = append(grouped[key], item)
	}
	for key, events := range grouped {
		body, err := gzipJSONLines(events)
		if err != nil {
			s.recordFailure(len(events), err, key.dataset)
			continue
		}
		digest := sha256.Sum256(body)
		now := time.Now().UTC()
		objectKey := fmt.Sprintf(
			"%s/%s/date=%s/%s-%s-%06d.jsonl.gz",
			s.config.Prefix,
			key.dataset,
			key.date,
			now.Format("20060102T150405.000000000Z"),
			s.instance,
			s.sequence.Add(1),
		)
		ctx, cancel := context.WithTimeout(context.Background(), s.config.PutTimeout)
		err = s.uploader.PutObject(ctx, PutRequest{
			Bucket:          s.config.Bucket,
			Key:             objectKey,
			Body:            body,
			ContentType:     "application/x-ndjson",
			ContentEncoding: "gzip",
			Metadata: map[string]string{
				"sha256":            hex.EncodeToString(digest[:]),
				"schema-version":    strconv.Itoa(SchemaVersion),
				"record-count":      strconv.Itoa(len(events)),
				"first-enqueued-at": events[0].enqueuedAt.UTC().Format(time.RFC3339Nano),
				"last-enqueued-at":  events[len(events)-1].enqueuedAt.UTC().Format(time.RFC3339Nano),
			},
		})
		cancel()
		if err != nil {
			s.recordFailure(len(events), err, key.dataset)
			continue
		}
		s.uploaded.Add(uint64(len(events)))
		s.lastPut.Store(time.Now().UTC().UnixNano())
	}
}

func (s *Shadow) recordFailure(records int, err error, dataset Dataset) {
	s.failed.Add(uint64(records))
	s.lastFail.Store(time.Now().UTC().UnixNano())
	slog.Warn("telemetry archive upload failed", "dataset", dataset, "records", records, "err", err)
}

func gzipJSONLines(events []event) ([]byte, error) {
	var output bytes.Buffer
	writer, err := gzip.NewWriterLevel(&output, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	encoder := json.NewEncoder(writer)
	for _, item := range events {
		if err := encoder.Encode(archiveRecord{
			SchemaVersion: SchemaVersion,
			Dataset:       item.dataset,
			EnqueuedAt:    item.enqueuedAt,
			Payload:       item.payload,
		}); err != nil {
			_ = writer.Close()
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (s *Shadow) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		s.lifecycle.Lock()
		defer s.lifecycle.Unlock()
		s.stopped.Store(true)
		if s.active() {
			close(s.queue)
		}
	})
	select {
	case <-s.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Shadow) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	return Stats{
		InstanceID:    s.instance,
		StartedAt:     s.startedAt,
		Enqueued:      s.enqueued.Load(),
		Dropped:       s.dropped.Load(),
		Uploaded:      s.uploaded.Load(),
		Failed:        s.failed.Load(),
		Pending:       s.pending.Load(),
		PendingBytes:  s.bytes.Load(),
		LastUploadAt:  unixNanoTime(s.lastPut.Load()),
		LastFailureAt: unixNanoTime(s.lastFail.Load()),
	}
}

func unixNanoTime(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}

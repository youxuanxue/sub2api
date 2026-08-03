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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
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
	BatchSize     int
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
	dataset Dataset
	payload json.RawMessage
}

type Stats struct {
	Enqueued uint64
	Dropped  uint64
	Uploaded uint64
	Failed   uint64
}

// Shadow is a bounded, best-effort raw telemetry writer. Enqueue never waits for
// S3 and Stop is only used during application shutdown.
type Shadow struct {
	config    Config
	uploader  Uploader
	queue     chan event
	stopCh    chan struct{}
	doneCh    chan struct{}
	stopOnce  sync.Once
	stopped   atomic.Bool
	lifecycle sync.RWMutex
	sequence  atomic.Uint64
	instance  string
	enqueued  atomic.Uint64
	dropped   atomic.Uint64
	uploaded  atomic.Uint64
	failed    atomic.Uint64
}

func New(config Config, uploader Uploader) *Shadow {
	config.Prefix = strings.Trim(strings.TrimSpace(config.Prefix), "/")
	if config.QueueSize <= 0 {
		config.QueueSize = 8192
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 256
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = 5 * time.Second
	}
	if config.PutTimeout <= 0 {
		config.PutTimeout = 10 * time.Second
	}
	shadow := &Shadow{
		config:   config,
		uploader: uploader,
		queue:    make(chan event, config.QueueSize),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
		instance: uuid.NewString(),
	}
	if !shadow.active() {
		close(shadow.doneCh)
		return shadow
	}
	go shadow.run()
	return shadow
}

func (s *Shadow) active() bool {
	return s != nil && s.config.Enabled && s.uploader != nil && s.config.Bucket != "" && s.config.Prefix != ""
}

func (s *Shadow) Enqueue(dataset Dataset, value any) bool {
	if !s.active() || s.stopped.Load() || !validDataset(dataset) {
		return false
	}
	payload, err := json.Marshal(value)
	if err != nil {
		s.recordDrop("json_marshal")
		return false
	}
	s.lifecycle.RLock()
	defer s.lifecycle.RUnlock()
	if s.stopped.Load() {
		return false
	}
	select {
	case s.queue <- event{dataset: dataset, payload: payload}:
		s.enqueued.Add(1)
		return true
	default:
		s.recordDrop("queue_full")
		return false
	}
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

func (s *Shadow) run() {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.config.FlushInterval)
	defer ticker.Stop()
	batch := make([]event, 0, s.config.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.flush(batch)
		batch = batch[:0]
	}
	for {
		select {
		case item := <-s.queue:
			batch = append(batch, item)
			if len(batch) >= s.config.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-s.stopCh:
			for {
				select {
				case item := <-s.queue:
					batch = append(batch, item)
					if len(batch) >= s.config.BatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (s *Shadow) flush(batch []event) {
	byDataset := make(map[Dataset][]json.RawMessage, 3)
	for _, item := range batch {
		byDataset[item.dataset] = append(byDataset[item.dataset], item.payload)
	}
	for dataset, payloads := range byDataset {
		body, err := gzipJSONLines(payloads)
		if err != nil {
			s.failed.Add(uint64(len(payloads)))
			continue
		}
		digest := sha256.Sum256(body)
		now := time.Now().UTC()
		key := fmt.Sprintf(
			"%s/%s/date=%s/%s-%s-%06d.jsonl.gz",
			s.config.Prefix,
			dataset,
			now.Format("2006-01-02"),
			now.Format("20060102T150405.000000000Z"),
			s.instance,
			s.sequence.Add(1),
		)
		ctx, cancel := context.WithTimeout(context.Background(), s.config.PutTimeout)
		err = s.uploader.PutObject(ctx, PutRequest{
			Bucket:          s.config.Bucket,
			Key:             key,
			Body:            body,
			ContentType:     "application/x-ndjson",
			ContentEncoding: "gzip",
			Metadata:        map[string]string{"sha256": hex.EncodeToString(digest[:])},
		})
		cancel()
		if err != nil {
			s.failed.Add(uint64(len(payloads)))
			slog.Warn("telemetry archive upload failed", "dataset", dataset, "records", len(payloads), "err", err)
			continue
		}
		s.uploaded.Add(uint64(len(payloads)))
	}
}

func gzipJSONLines(payloads []json.RawMessage) ([]byte, error) {
	var output bytes.Buffer
	writer, err := gzip.NewWriterLevel(&output, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	for _, payload := range payloads {
		if _, err := writer.Write(payload); err != nil {
			_ = writer.Close()
			return nil, err
		}
		if _, err := writer.Write([]byte{'\n'}); err != nil {
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
			close(s.stopCh)
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
		Enqueued: s.enqueued.Load(),
		Dropped:  s.dropped.Load(),
		Uploaded: s.uploaded.Load(),
		Failed:   s.failed.Load(),
	}
}

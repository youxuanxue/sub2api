package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/telemetryarchive"
)

const (
	telemetryArchiveHeartbeatJobName  = "telemetry_archive_shadow"
	telemetryArchiveHeartbeatInterval = time.Minute
	telemetryArchiveHeartbeatTimeout  = 2 * time.Second
)

type telemetryArchiveHeartbeatRepository interface {
	UpsertJobHeartbeat(context.Context, *OpsUpsertJobHeartbeatInput) error
}

// TelemetryArchiveHealth publishes the shadow writer's cumulative process
// counters. Any loss keeps the process heartbeat unhealthy until restart.
type TelemetryArchiveHealth struct {
	shadow   *telemetryarchive.Shadow
	repo     telemetryArchiveHeartbeatRepository
	interval time.Duration
	timeout  time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
	started  atomic.Bool
	start    sync.Once
	stop     sync.Once
}

func NewTelemetryArchiveHealth(
	shadow *telemetryarchive.Shadow,
	repo telemetryArchiveHeartbeatRepository,
) *TelemetryArchiveHealth {
	return newTelemetryArchiveHealth(
		shadow,
		repo,
		telemetryArchiveHeartbeatInterval,
		telemetryArchiveHeartbeatTimeout,
	)
}

func newTelemetryArchiveHealth(
	shadow *telemetryarchive.Shadow,
	repo telemetryArchiveHeartbeatRepository,
	interval time.Duration,
	timeout time.Duration,
) *TelemetryArchiveHealth {
	return &TelemetryArchiveHealth{
		shadow:   shadow,
		repo:     repo,
		interval: interval,
		timeout:  timeout,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

func (s *TelemetryArchiveHealth) Enabled() bool {
	return s != nil && s.shadow != nil && s.shadow.Enabled() && s.repo != nil
}

func (s *TelemetryArchiveHealth) Start() {
	if !s.Enabled() {
		return
	}
	s.start.Do(func() {
		s.started.Store(true)
		go s.run()
	})
}

func (s *TelemetryArchiveHealth) run() {
	defer close(s.doneCh)
	s.publish()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.publish()
		case <-s.stopCh:
			return
		}
	}
}

func (s *TelemetryArchiveHealth) publish() {
	stats := s.shadow.Stats()
	result, err := json.Marshal(stats)
	if err != nil {
		slog.Warn("telemetry archive health serialization failed", "err", err)
		return
	}

	now := time.Now().UTC()
	resultText := string(result)
	input := &OpsUpsertJobHeartbeatInput{
		JobName:    telemetryArchiveHeartbeatJobName,
		LastRunAt:  &now,
		LastResult: &resultText,
	}
	if stats.Dropped > 0 || stats.Failed > 0 {
		message := fmt.Sprintf("dropped=%d failed=%d", stats.Dropped, stats.Failed)
		input.LastErrorAt = &now
		input.LastError = &message
	} else {
		input.LastSuccessAt = &now
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := s.repo.UpsertJobHeartbeat(ctx, input); err != nil {
		slog.Warn("telemetry archive health heartbeat failed", "err", err)
	}
}

func (s *TelemetryArchiveHealth) Stop(ctx context.Context) error {
	if s == nil || !s.started.Load() {
		return nil
	}
	s.stop.Do(func() { close(s.stopCh) })
	select {
	case <-s.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

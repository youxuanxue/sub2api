package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type terminalOutcomeRepoStub struct {
	mu        sync.Mutex
	failCount int
	flushes   []TerminalOutcomeMinuteFlush
}

func (s *terminalOutcomeRepoStub) FlushMinute(_ context.Context, flush TerminalOutcomeMinuteFlush) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushes = append(s.flushes, flush)
	if s.failCount > 0 {
		s.failCount--
		return errors.New("database unavailable")
	}
	return nil
}

func TestTerminalOutcomeRecorderConservesAcceptedEvents(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	repo := &terminalOutcomeRepoStub{}
	recorder := newTerminalOutcomeRecorder(repo, 4, func() time.Time { return now }, "epoch-a")

	require.True(t, recorder.Record(TerminalOutcomeEvent{At: now, GroupID: 7, RequestedModel: " gpt-5.4 ", Kind: TerminalOutcomeSuccess}))
	require.True(t, recorder.Record(TerminalOutcomeEvent{At: now, GroupID: 7, RequestedModel: "gpt-5.4", Kind: TerminalOutcomeFinalEmptyPool429}))
	now = now.Add(2 * time.Minute)
	require.NoError(t, recorder.flushReadyMinutes(context.Background()))

	require.Len(t, repo.flushes, 1)
	flush := repo.flushes[0]
	require.Equal(t, int64(2), flush.Health.SeenCount)
	require.Equal(t, int64(2), flush.Health.PersistedCount)
	require.Zero(t, flush.Health.DropCount)
	require.True(t, flush.Health.Complete)
	require.Equal(t, []TerminalOutcomeFact{{
		BucketStart:       time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
		GroupID:           7,
		RequestedModel:    "gpt-5.4",
		ProducerEpoch:     "epoch-a",
		SuccessCount:      1,
		EmptyPool429Count: 1,
	}}, flush.Facts)
}

func TestTerminalOutcomeRecorderMarksStartupPartialMinuteIncomplete(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 10, 0, time.UTC)
	startedAt := now
	repo := &terminalOutcomeRepoStub{}
	recorder := newTerminalOutcomeRecorder(repo, 1, func() time.Time { return now }, "epoch-a")
	require.True(t, recorder.Record(TerminalOutcomeEvent{At: now, RequestedModel: "gpt-5.4", Kind: TerminalOutcomeSuccess}))
	now = now.Add(2 * time.Minute)

	require.NoError(t, recorder.flushReadyMinutes(context.Background()))
	require.Len(t, repo.flushes, 1)
	health := repo.flushes[0].Health
	require.False(t, health.Complete)
	require.Equal(t, startedAt, health.ProcessStartedAt)
	require.Equal(t, now, health.ClosedAt)
	require.Equal(t, int64(1), health.FlushSequence)
}

func TestTerminalOutcomeRecorderMarksDroppedMinuteIncomplete(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 10, 0, time.UTC)
	repo := &terminalOutcomeRepoStub{}
	recorder := newTerminalOutcomeRecorder(repo, 1, func() time.Time { return now }, "epoch-a")
	event := TerminalOutcomeEvent{At: now, GroupID: 7, RequestedModel: "gpt-5.4", Kind: TerminalOutcomeSuccess}

	require.True(t, recorder.Record(event))
	require.False(t, recorder.Record(event))
	now = now.Add(2 * time.Minute)
	require.NoError(t, recorder.flushReadyMinutes(context.Background()))

	require.Len(t, repo.flushes, 1)
	require.Equal(t, int64(2), repo.flushes[0].Health.SeenCount)
	require.Equal(t, int64(1), repo.flushes[0].Health.PersistedCount)
	require.Equal(t, int64(1), repo.flushes[0].Health.DropCount)
	require.False(t, repo.flushes[0].Health.Complete)
}

func TestTerminalOutcomeRecorderRetriesCumulativeSnapshotWithoutDoubleCount(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 10, 0, time.UTC)
	repo := &terminalOutcomeRepoStub{failCount: 1}
	recorder := newTerminalOutcomeRecorder(repo, 2, func() time.Time { return now }, "epoch-a")
	require.True(t, recorder.Record(TerminalOutcomeEvent{At: now, GroupID: 7, RequestedModel: "claude-sonnet-4-6", Kind: TerminalOutcomeOtherError}))
	now = now.Add(2 * time.Minute)

	require.Error(t, recorder.flushReadyMinutes(context.Background()))
	require.NoError(t, recorder.flushReadyMinutes(context.Background()))
	require.Len(t, repo.flushes, 2)
	require.Equal(t, int64(1), repo.flushes[0].Facts[0].OtherErrorCount)
	require.Equal(t, int64(1), repo.flushes[1].Facts[0].OtherErrorCount)
	require.Equal(t, int64(1), repo.flushes[1].Health.FlushFailureCount)
	require.False(t, repo.flushes[1].Health.Complete)
}

func TestTerminalOutcomeRecorderWritesZeroHeartbeat(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	repo := &terminalOutcomeRepoStub{}
	recorder := newTerminalOutcomeRecorder(repo, 2, func() time.Time { return now }, "epoch-a")
	now = now.Add(2 * time.Minute)

	require.NoError(t, recorder.flushReadyMinutes(context.Background()))
	require.Len(t, repo.flushes, 1)
	require.Empty(t, repo.flushes[0].Facts)
	require.True(t, repo.flushes[0].Health.Complete)
	require.Zero(t, repo.flushes[0].Health.SeenCount)
}

func TestTerminalOutcomeRecorderKeepsProducerEpochsSeparate(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 10, 0, time.UTC)
	repo := &terminalOutcomeRepoStub{}
	first := newTerminalOutcomeRecorder(repo, 1, func() time.Time { return now }, "epoch-a")
	second := newTerminalOutcomeRecorder(repo, 1, func() time.Time { return now }, "epoch-b")
	event := TerminalOutcomeEvent{At: now, RequestedModel: "gpt-5.4", Kind: TerminalOutcomeSuccess}
	require.True(t, first.Record(event))
	require.True(t, second.Record(event))
	now = now.Add(2 * time.Minute)

	require.NoError(t, first.flushReadyMinutes(context.Background()))
	require.NoError(t, second.flushReadyMinutes(context.Background()))
	require.Equal(t, "epoch-a", repo.flushes[0].Health.ProducerEpoch)
	require.Equal(t, "epoch-b", repo.flushes[1].Health.ProducerEpoch)
}

package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// A hold handed off to settlement is only safe while something is guaranteed to
// consume it. The usage-record pool may DROP a task on overflow, in which case
// the bill never runs — so the submit site must compensate the hold, or the
// reservation pins real user balance until the 30-minute reconciler TTL.
//
// This is the shape of the production incident: a burst of requests, one of
// which lost the queue race and left a large reservation stranded while its
// siblings settled normally.

type droppedHoldRepository struct {
	service.UsageBillingRepository
	mu       sync.Mutex
	released []string
}

func (r *droppedHoldRepository) ReserveBalanceHold(_ context.Context, _ *service.HoldCommand) (bool, error) {
	return true, nil
}

func (r *droppedHoldRepository) ReleaseBalanceHold(_ context.Context, requestID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, requestID)
	return true, nil
}

func (r *droppedHoldRepository) ReleaseExpiredBalanceHolds(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

func (r *droppedHoldRepository) releasedKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.released...)
}

// saturatedDropPool is a pool whose queue is already full, so every Submit
// returns the dropped mode deterministically (policy=drop, no sync fallback).
func saturatedDropPool(t *testing.T) *service.UsageRecordWorkerPool {
	t.Helper()
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	t.Cleanup(pool.Stop)

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	// Occupy the single worker and then the single queue slot.
	for i := 0; i < 8; i++ {
		pool.Submit(func(context.Context) { <-block })
	}
	return pool
}

func TestSubmitUsageRecordTask_DroppedTaskReleasesHandedOffHold(t *testing.T) {
	repo := &droppedHoldRepository{}
	gateway := service.NewOpenAIGatewayService(nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &OpenAIGatewayHandler{gatewayService: gateway, usageRecordWorkerPool: saturatedDropPool(t)}

	hold := &tkHoldHandle{h: h, ctx: context.Background(), requestID: "local:dropped-task"}
	holdRequestID := hold.HandOffToSettlement()
	require.Equal(t, "local:dropped-task", holdRequestID)

	ran := false
	h.submitOpenAIUsageRecordTask(
		context.Background(),
		&service.OpenAIForwardResult{},
		func(context.Context) { ran = true },
		h.tkReleaseHoldOnDroppedTask(context.Background(), holdRequestID),
	)

	require.False(t, ran, "a saturated drop pool must not run the task")
	require.Equal(t, []string{"local:dropped-task"}, repo.releasedKeys(),
		"a dropped usage-record task must release the hold it will never settle")
	// The deferred handler release stays a no-op: ownership moved at hand-off and
	// the drop compensation already refunded.
	hold.ReleaseUnlessSettling()
	require.Equal(t, []string{"local:dropped-task"}, repo.releasedKeys(),
		"compensation must not be followed by a second refund")
}

// Media bills route through the mandatory path, which falls back to inline
// execution rather than dropping — so the hold must be settled by the task, not
// compensated.
func TestSubmitUsageRecordTask_MandatoryPathStillRunsTask(t *testing.T) {
	repo := &droppedHoldRepository{}
	gateway := service.NewOpenAIGatewayService(nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &OpenAIGatewayHandler{gatewayService: gateway, usageRecordWorkerPool: saturatedDropPool(t)}

	ran := false
	h.submitOpenAIUsageRecordTask(
		context.Background(),
		&service.OpenAIForwardResult{ImageCount: 1},
		func(context.Context) { ran = true },
		h.tkReleaseHoldOnDroppedTask(context.Background(), "local:image-bill"),
	)

	require.True(t, ran, "money-critical media bills must never be dropped")
	require.Empty(t, repo.releasedKeys(), "an executed task settles its own hold")
}

func TestTkReleaseHoldOnDroppedTask_NilSafe(t *testing.T) {
	var h *OpenAIGatewayHandler
	require.Nil(t, h.tkReleaseHoldOnDroppedTask(context.Background(), "local:x"))

	withService := &OpenAIGatewayHandler{gatewayService: &service.OpenAIGatewayService{}}
	require.Nil(t, withService.tkReleaseHoldOnDroppedTask(context.Background(), ""),
		"an empty hold key needs no compensation")

	// A nil compensation entry must not panic the drop path.
	onUsageRecordTaskDropped([]func(){nil})
}

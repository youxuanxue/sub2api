package service

import (
	"context"
	"time"
)

type SchedulerOutboxEvent struct {
	ID         int64
	EventType  string
	AccountID  *int64
	GroupID    *int64
	Payload    map[string]any
	CreatedAt  time.Time
	ClaimToken string
	Attempts   int
}

// SchedulerOutboxRepository 提供调度 outbox 的读取接口。
type SchedulerOutboxRepository interface {
	ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]SchedulerOutboxEvent, error)
	RenewClaim(ctx context.Context, id int64, token string, lease time.Duration) (bool, error)
	Acknowledge(ctx context.Context, id int64, token string) (bool, error)
	PendingCount(ctx context.Context) (int64, error)
	TryAcquireDeliveryLock(ctx context.Context) (SchedulerOutboxDeliveryLease, bool, error)
	// FirstCreatedAtAfter returns the oldest unacknowledged event. afterID is
	// retained for diagnostic callers but does not restrict delivery completeness.
	FirstCreatedAtAfter(ctx context.Context, afterID int64) (time.Time, bool, error)
	MaxID(ctx context.Context) (int64, error)
	DeleteConsumedUpTo(ctx context.Context, watermark int64, limit int) (int64, error)
	TryAcquireCleanupLock(ctx context.Context) (SchedulerOutboxCleanupLease, bool, error)
}

type SchedulerOutboxDeliveryLease interface {
	Context() context.Context
	Generation() int64
	Release()
}

// Production cache implementations fence every projection mutation in Redis.
type SchedulerOutboxProjectionFencer interface {
	BeginOutboxProjection(context.Context, int64) (context.Context, error)
}

type schedulerOutboxProjectionKey struct{}

func isSchedulerOutboxProjection(ctx context.Context) bool {
	guarded, _ := ctx.Value(schedulerOutboxProjectionKey{}).(bool)
	return guarded
}

// SchedulerOutboxCleanupLease holds the PostgreSQL advisory lock used by
// scheduler outbox cleanup.
type SchedulerOutboxCleanupLease interface {
	Release()
}

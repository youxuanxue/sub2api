package repository

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// ListAfter exposes only submit events to the Redis publisher. Terminal
// notifications stay durable in PostgreSQL but never become executable jobs.
// The cursor makes replay bounded while queue idempotency handles a crash
// between enqueue and cursor advancement.
func (r *ManagedBatchRepository) ListAfter(ctx context.Context, after int64, limit int) ([]service.ManagedBatchOutboxEvent, error) {
	if r == nil || r.db == nil {
		return nil, service.ErrManagedBatchOutboxStalled
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,batch_id,payload FROM batch_outbox WHERE id>$1 AND event_type='batch.submit' ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ManagedBatchOutboxEvent, 0, limit)
	for rows.Next() {
		var id int64
		var batchID string
		var raw []byte
		if err := rows.Scan(&id, &batchID, &raw); err != nil {
			return nil, err
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		if strings.TrimSpace(batchID) == "" {
			continue
		}
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		out = append(out, service.ManagedBatchOutboxEvent{ID: id, Job: service.ManagedBatchJob{ID: batchID, Payload: payloadBytes}})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ManagedBatchOutboxCursor stores the last successfully enqueued outbox ID
// in Redis. It is independent from the job queue namespace.
type ManagedBatchOutboxCursor struct {
	rdb *redis.Client
	key string
}

func NewManagedBatchOutboxCursor(rdb *redis.Client, prefix string) *ManagedBatchOutboxCursor {
	if prefix == "" {
		prefix = "managed_batch:v1"
	}
	return &ManagedBatchOutboxCursor{rdb: rdb, key: prefix + ":outbox_cursor"}
}

func (c *ManagedBatchOutboxCursor) Load(ctx context.Context) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, service.ErrManagedBatchOutboxStalled
	}
	raw, err := c.rdb.Get(ctx, c.key).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, service.ErrManagedBatchOutboxStalled
	}
	return value, nil
}

func (c *ManagedBatchOutboxCursor) Store(ctx context.Context, id int64) error {
	if c == nil || c.rdb == nil || id < 0 {
		return service.ErrManagedBatchOutboxStalled
	}
	return c.rdb.Set(ctx, c.key, strconv.FormatInt(id, 10), 0).Err()
}

var _ service.ManagedBatchOutbox = (*ManagedBatchRepository)(nil)
var _ service.ManagedBatchOutboxCursor = (*ManagedBatchOutboxCursor)(nil)

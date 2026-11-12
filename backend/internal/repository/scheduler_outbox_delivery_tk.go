package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

func (r *schedulerOutboxRepository) ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]service.SchedulerOutboxEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if lease <= 0 {
		return nil, fmt.Errorf("invalid outbox lease duration")
	}
	var pending bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM scheduler_outbox
		WHERE acknowledged_at IS NULL AND (lease_until IS NULL OR lease_until <= clock_timestamp())
	)`).Scan(&pending); err != nil {
		return nil, err
	}
	if !pending {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH selected AS MATERIALIZED (
			SELECT id FROM scheduler_outbox
			WHERE acknowledged_at IS NULL AND (lease_until IS NULL OR lease_until <= clock_timestamp())
			ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED
		), claimed AS (
			UPDATE scheduler_outbox o
			SET claim_token = $2, lease_until = clock_timestamp() + $3 * interval '1 millisecond',
			    attempts = attempts + 1, dedup_key = NULL
			FROM selected WHERE o.id = selected.id
			RETURNING o.id, o.event_type, o.account_id, o.group_id, o.payload, o.created_at, o.claim_token, o.attempts
		)
		SELECT * FROM claimed ORDER BY id
	`, limit, uuid.NewString(), lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var events []service.SchedulerOutboxEvent
	for rows.Next() {
		var e service.SchedulerOutboxEvent
		var accountID, groupID sql.NullInt64
		var raw []byte
		if err := rows.Scan(&e.ID, &e.EventType, &accountID, &groupID, &raw, &e.CreatedAt, &e.ClaimToken, &e.Attempts); err != nil {
			return nil, err
		}
		if accountID.Valid {
			e.AccountID = &accountID.Int64
		}
		if groupID.Valid {
			e.GroupID = &groupID.Int64
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &e.Payload); err != nil {
				return nil, err
			}
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (r *schedulerOutboxRepository) RenewClaim(ctx context.Context, id int64, token string, lease time.Duration) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE scheduler_outbox
		SET lease_until = clock_timestamp() + $3 * interval '1 millisecond'
		WHERE id = $1 AND claim_token = $2 AND acknowledged_at IS NULL`, id, token, lease.Milliseconds())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *schedulerOutboxRepository) Acknowledge(ctx context.Context, id int64, token string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE scheduler_outbox
		SET acknowledged_at = clock_timestamp(), lease_until = NULL
		WHERE id = $1 AND claim_token = $2 AND acknowledged_at IS NULL
		AND lease_until > clock_timestamp()`, id, token)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (r *schedulerOutboxRepository) PendingCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE acknowledged_at IS NULL`).Scan(&count)
	return count, err
}

type schedulerDeliveryLease struct {
	generation int64
	conn       *sql.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	once       sync.Once
}

func (r *schedulerOutboxRepository) TryAcquireDeliveryLock(ctx context.Context) (service.SchedulerOutboxDeliveryLease, bool, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext('scheduler_outbox_delivery'))`).Scan(&acquired); err != nil || !acquired {
		_ = conn.Close()
		return nil, false, err
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	lease := &schedulerDeliveryLease{conn: conn, ctx: leaseCtx, cancel: cancel, done: make(chan struct{})}
	if err := conn.QueryRowContext(ctx, `SELECT nextval('scheduler_outbox_projection_generation')`).Scan(&lease.generation); err != nil {
		cancel()
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
		return nil, false, err
	}
	// Stop all projection work when the lock-owning connection is lost. Do not
	// let an expired row lease create concurrent same-scope projectors while the
	// original process still owns the advisory lock.
	go func() {
		defer close(lease.done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				pingCtx, stop := context.WithTimeout(leaseCtx, time.Second)
				err := conn.PingContext(pingCtx)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return lease, true, nil
}

func (l *schedulerDeliveryLease) Context() context.Context { return l.ctx }
func (l *schedulerDeliveryLease) Generation() int64        { return l.generation }

func (l *schedulerDeliveryLease) Release() {
	l.once.Do(func() {
		l.cancel()
		<-l.done
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := l.conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext('scheduler_outbox_delivery'))`); err != nil {
			_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = l.conn.Close()
	})
}

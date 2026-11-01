package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// ManagedBatchQueueOptions names are versioned so a future queue can coexist
// with the existing image queue during rollout.
type ManagedBatchQueueOptions struct {
	Prefix      string
	InflightTTL time.Duration
	LeaseTTL    time.Duration
}

const (
	managedBatchDefaultPrefix   = "managed_batch:v1"
	managedBatchDefaultTTL      = 7 * 24 * time.Hour
	managedBatchDefaultLeaseTTL = 5 * time.Minute
)

type managedBatchQueue struct {
	rdb                        *redis.Client
	ready, delayed, active     string
	inflight, payloads, leases string
	inflightTTL, leaseTTL      time.Duration
}

func NewManagedBatchQueue(rdb *redis.Client, opts ManagedBatchQueueOptions) service.ManagedBatchQueue {
	if opts.Prefix == "" {
		opts.Prefix = managedBatchDefaultPrefix
	}
	if opts.InflightTTL <= 0 {
		opts.InflightTTL = managedBatchDefaultTTL
	}
	if opts.LeaseTTL <= 0 {
		opts.LeaseTTL = managedBatchDefaultLeaseTTL
	}
	return &managedBatchQueue{
		rdb:   rdb,
		ready: opts.Prefix + ":ready", delayed: opts.Prefix + ":delayed", active: opts.Prefix + ":active",
		inflight: opts.Prefix + ":inflight:", payloads: opts.Prefix + ":payload:", leases: opts.Prefix + ":lease:",
		inflightTTL: opts.InflightTTL, leaseTTL: opts.LeaseTTL,
	}
}

var managedBatchEnqueueScript = redis.NewScript(`
if redis.call("SET", KEYS[1], ARGV[1], "NX", "PX", ARGV[2]) then
  redis.call("SET", KEYS[3], ARGV[3], "PX", ARGV[2])
  redis.call("LPUSH", KEYS[2], ARGV[1])
  return 1
end
return 0
`)

var managedBatchReserveScript = redis.NewScript(`
local job = redis.call("RPOP", KEYS[1])
if not job then return nil end
redis.call("ZADD", KEYS[2], ARGV[1], job)
return job
`)

var managedBatchMoveScript = redis.NewScript(`
local jobs = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", ARGV[1], "LIMIT", 0, ARGV[2])
for _, job in ipairs(jobs) do redis.call("ZREM", KEYS[1], job); redis.call("LPUSH", KEYS[2], job) end
return #jobs
`)

var managedBatchReleaseLeaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end
return 0
`)

var managedBatchRefreshLeaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("PEXPIRE", KEYS[1], ARGV[2]) end
return 0
`)

func (q *managedBatchQueue) Enqueue(ctx context.Context, job service.ManagedBatchJob) error {
	if !validManagedBatchJob(job) {
		return service.ErrManagedBatchInvalidJob
	}
	n, err := managedBatchEnqueueScript.Run(ctx, q.rdb, []string{q.inflightKey(job.ID), q.ready, q.payloadKey(job.ID)}, job.ID, q.inflightTTL.Milliseconds(), hex.EncodeToString(job.Payload)).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrManagedBatchAlreadyQueued
	}
	return nil
}

func (q *managedBatchQueue) Reserve(ctx context.Context, block time.Duration) (service.ManagedBatchDelivery, error) {
	deadline := time.Now().Add(block)
	for {
		raw, err := managedBatchReserveScript.Run(ctx, q.rdb, []string{q.ready, q.active}, time.Now().UnixMilli()).Result()
		if err == nil {
			id, ok := raw.(string)
			if !ok || id == "" {
				return service.ManagedBatchDelivery{}, service.ErrManagedBatchInvalidJob
			}
			payloadHex, getErr := q.rdb.Get(ctx, q.payloadKey(id)).Result()
			if getErr != nil {
				_ = q.rdb.ZRem(ctx, q.active, id).Err()
				return service.ManagedBatchDelivery{}, getErr
			}
			payload, decodeErr := hex.DecodeString(payloadHex)
			if decodeErr != nil {
				_ = q.rdb.ZRem(ctx, q.active, id).Err()
				return service.ManagedBatchDelivery{}, service.ErrManagedBatchInvalidJob
			}
			job := service.ManagedBatchJob{ID: id, Payload: payload}
			return service.ManagedBatchDelivery{Job: job, ReservedAt: time.Now()}, nil
		}
		if !errors.Is(err, redis.Nil) {
			return service.ManagedBatchDelivery{}, err
		}
		if block <= 0 || time.Now().After(deadline) {
			return service.ManagedBatchDelivery{}, service.ErrManagedBatchQueueEmpty
		}
		d := time.Until(deadline)
		if d > 50*time.Millisecond {
			d = 50 * time.Millisecond
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return service.ManagedBatchDelivery{}, ctx.Err()
		case <-t.C:
		}
	}
}

func (q *managedBatchQueue) Ack(ctx context.Context, id string) error {
	if id == "" {
		return service.ErrManagedBatchInvalidJob
	}
	p := q.rdb.TxPipeline()
	p.ZRem(ctx, q.active, id)
	p.ZRem(ctx, q.delayed, id)
	p.Del(ctx, q.inflightKey(id))
	p.Del(ctx, q.payloadKey(id))
	_, err := p.Exec(ctx)
	return err
}

func (q *managedBatchQueue) Requeue(ctx context.Context, id string, delay time.Duration) error {
	if id == "" {
		return service.ErrManagedBatchInvalidJob
	}
	p := q.rdb.TxPipeline()
	p.ZRem(ctx, q.active, id)
	p.ZRem(ctx, q.delayed, id)
	if delay > 0 {
		p.ZAdd(ctx, q.delayed, redis.Z{Score: float64(time.Now().Add(delay).UnixMilli()), Member: id})
	} else {
		p.LPush(ctx, q.ready, id)
	}
	_, err := p.Exec(ctx)
	return err
}

func (q *managedBatchQueue) Heartbeat(ctx context.Context, id string) error {
	if id == "" {
		return service.ErrManagedBatchInvalidJob
	}
	return q.rdb.ZAddXX(ctx, q.active, redis.Z{Score: float64(time.Now().UnixMilli()), Member: id}).Err()
}

func (q *managedBatchQueue) RecoverStale(ctx context.Context, stale time.Duration, limit int) (int, error) {
	if stale <= 0 {
		return 0, service.ErrManagedBatchInvalidJob
	}
	if limit <= 0 {
		limit = 100
	}
	return managedBatchMoveScript.Run(ctx, q.rdb, []string{q.active, q.ready}, time.Now().Add(-stale).UnixMilli(), limit).Int()
}

// MoveDelayed promotes due retries back to the ready list. It is kept off the
// base queue interface so alternate implementations can opt in independently.
func (q *managedBatchQueue) MoveDelayed(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	return managedBatchMoveScript.Run(ctx, q.rdb, []string{q.delayed, q.ready}, time.Now().UnixMilli(), limit).Int()
}

func (q *managedBatchQueue) TryAcquireJobLease(ctx context.Context, id string, ttl time.Duration) (service.ManagedBatchLease, bool, error) {
	if id == "" {
		return nil, false, service.ErrManagedBatchInvalidJob
	}
	if ttl <= 0 {
		ttl = q.leaseTTL
	}
	token, err := randomManagedBatchToken()
	if err != nil {
		return nil, false, err
	}
	ok, err := q.rdb.SetNX(ctx, q.leases+id, token, ttl).Result()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &managedBatchRedisLease{rdb: q.rdb, key: q.leases + id, token: token}, true, nil
}

func (q *managedBatchQueue) inflightKey(id string) string { return q.inflight + id }
func (q *managedBatchQueue) payloadKey(id string) string  { return q.payloads + id }

func validManagedBatchJob(job service.ManagedBatchJob) bool {
	return job.ID != "" && len(job.ID) <= 512 && !containsPipe(job.ID)
}
func containsPipe(s string) bool {
	for _, r := range s {
		if r == '|' {
			return true
		}
	}
	return false
}
func randomManagedBatchToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

type managedBatchRedisLease struct {
	rdb        *redis.Client
	key, token string
}

func (l *managedBatchRedisLease) Refresh(ctx context.Context, ttl time.Duration) error {
	if ttl <= 0 {
		return service.ErrManagedBatchLeaseLost
	}
	n, err := managedBatchRefreshLeaseScript.Run(ctx, l.rdb, []string{l.key}, l.token, ttl.Milliseconds()).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrManagedBatchLeaseLost
	}
	return nil
}
func (l *managedBatchRedisLease) Release(ctx context.Context) error {
	n, err := managedBatchReleaseLeaseScript.Run(ctx, l.rdb, []string{l.key}, l.token).Int()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrManagedBatchLeaseLost
	}
	return nil
}

var _ service.ManagedBatchQueue = (*managedBatchQueue)(nil)

package handler

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Coalescing of client-owned, self-inflicted limit rejections.
//
// Measured problem (prod, 2026): one api key at its own quota ceiling, retried
// by a Python-urllib client with no backoff at ~356 req/min for 90+ minutes,
// produced 29,731 of the 30,318 ops_error_logs rows in 24h (98%). Today's
// partition was 46 MB against 176 kB for an empty one, and the 587 rows of real
// incidents were unfindable inside it. The rejection decision is correct; what
// is wrong is writing one indistinguishable row per repeat.
//
// These rows carry no per-request information: same key, same code, same 429,
// same message, and no upstream was ever contacted. For triage one row per
// (api_key_id, code) per bucket plus the repeat count is equivalent to all of
// them.
//
// Properties this relies on, each covered by a test:
//   - The first occurrence in a bucket is always written, immediately. Nothing
//     is queued, delayed or batched, so a genuine incident never waits.
//   - Repeats inside the bucket are dropped, and their count is carried onto
//     the next bucket's first row, so no occurrence is silently lost.
//   - Buckets are per (api_key_id, code): another key, or another code on the
//     same key, has its own bucket and is never suppressed by this one.
//   - Memory is bounded and fails open: at capacity an unknown bucket is not
//     coalesced, i.e. the row is written. Pressure costs rows, never signal.

const (
	// opsCoalesceBucket is the suppression window. One row per key per code per
	// minute keeps a storm legible (60 rows/h instead of ~21k) while still
	// showing a per-minute pulse that it is ongoing.
	opsCoalesceBucket = time.Minute

	// opsCoalesceShardCount follows invalid_auth_abuse_limiter: fixed shards,
	// FNV-1a over the bucket key, no global lock on the reject path.
	opsCoalesceShardCount = 16

	// opsCoalesceCapacity bounds tracked buckets. One entry per (api_key_id,
	// code) actively being rejected; past this the gate fails open rather than
	// growing. Sized well above any plausible count of simultaneously
	// rate-limited keys.
	opsCoalesceCapacity = 4096
)

type opsCoalesceEntry struct {
	bucketStart time.Time
	// written records that the current bucket's first occurrence was already
	// written, so further occurrences in it are repeats.
	written bool
	// repeats counts occurrences dropped in the current bucket.
	repeats int64
	// carried counts occurrences dropped in buckets already closed and not yet
	// reported on a written row.
	carried int64
}

type opsCoalesceShard struct {
	mu      sync.Mutex
	entries map[string]*opsCoalesceEntry
}

type opsCoalescer struct {
	bucket   time.Duration
	capacity int64
	shards   [opsCoalesceShardCount]opsCoalesceShard
	now      func() time.Time

	tracked       atomic.Int64
	admitted      atomic.Uint64
	suppressed    atomic.Uint64
	overCapacity  atomic.Uint64
	cleanupNext   atomic.Int64
	cleanupCursor atomic.Uint32
}

// OpsErrorLogCoalesceHealth exposes the gate's counters for ops visibility.
// suppressed is the row count this process did not write.
type OpsErrorLogCoalesceHealth struct {
	Tracked      int64  `json:"tracked"`
	Capacity     int64  `json:"capacity"`
	Admitted     uint64 `json:"admitted"`
	Suppressed   uint64 `json:"suppressed"`
	OverCapacity uint64 `json:"over_capacity"`
}

func newOpsCoalescer(bucket time.Duration, capacity int64, now func() time.Time) *opsCoalescer {
	if now == nil {
		now = time.Now
	}
	c := &opsCoalescer{bucket: bucket, capacity: capacity, now: now}
	for i := range c.shards {
		c.shards[i].entries = make(map[string]*opsCoalesceEntry)
	}
	return c
}

var opsErrorLogCoalescer = newOpsCoalescer(opsCoalesceBucket, opsCoalesceCapacity, nil)

// swapOpsErrorLogCoalescer replaces the process-wide gate and returns a restore
// func. Test-only seam: the middleware reads the package-level instance, so a
// test that drives it needs a clean, clock-controlled one.
func swapOpsErrorLogCoalescer(c *opsCoalescer) func() {
	previous := opsErrorLogCoalescer
	opsErrorLogCoalescer = c
	return func() { opsErrorLogCoalescer = previous }
}

// OpsErrorLogCoalesceHealthSnapshot reports the coalescer's counters.
func OpsErrorLogCoalesceHealthSnapshot() OpsErrorLogCoalesceHealth {
	return opsErrorLogCoalescer.health()
}

// maybeCoalesceOpsErrorLogEntry decides whether this row is written.
//
// It returns false only for a repeat inside an open bucket. When it returns
// true and prior repeats were suppressed, the entry's message is annotated with
// that count before it is handed to the queue — the count rides in the existing
// error_message column, because ops_error_logs has a declared==written column
// contract (docs/approved/ops-error-logs-column-contract.md) and a new column
// would need DDL plus a writer on a 18-index high-write partitioned table.
func maybeCoalesceOpsErrorLogEntry(entry *service.OpsInsertErrorLogInput, code string) bool {
	return opsErrorLogCoalescer.admit(entry, code)
}

func (c *opsCoalescer) admit(entry *service.OpsInsertErrorLogInput, code string) bool {
	if c == nil || entry == nil {
		return true
	}
	key, ok := opsCoalesceKey(entry, code)
	if !ok {
		return true
	}
	now := c.now()
	c.maybeCleanupAtCapacity(now)

	shard := c.shard(key)
	shard.mu.Lock()
	existing := shard.entries[key]
	if existing != nil && now.Sub(existing.bucketStart) >= c.bucket {
		// Bucket closed: this occurrence opens the next one and reports
		// everything dropped while earlier buckets were open.
		existing.carried += existing.repeats
		existing.repeats = 0
		existing.written = false
		existing.bucketStart = now
	}
	if existing == nil {
		if !c.reserveEntry() {
			shard.mu.Unlock()
			// Fail open: at capacity an untracked bucket is written, not dropped.
			c.overCapacity.Add(1)
			c.admitted.Add(1)
			return true
		}
		existing = &opsCoalesceEntry{bucketStart: now}
		shard.entries[key] = existing
	}
	if existing.written {
		// Inside an open bucket whose first occurrence is already written.
		existing.repeats++
		shard.mu.Unlock()
		c.suppressed.Add(1)
		return false
	}
	// First occurrence of this bucket: write it now, carrying the repeats
	// accumulated while earlier buckets were open.
	existing.written = true
	carried := existing.carried
	existing.carried = 0
	shard.mu.Unlock()

	c.admitted.Add(1)
	if carried > 0 {
		annotateOpsCoalescedRepeats(entry, carried)
	}
	return true
}

// opsCoalesceKey restricts coalescing to rows that carry no per-request
// information: a client-owned, business-limited local limit rejection bound to
// a known api key, with no upstream attribution. Anything else — a different
// owner, an upstream error, a row without an api key — is never coalesced.
func opsCoalesceKey(entry *service.OpsInsertErrorLogInput, code string) (string, bool) {
	if entry == nil || entry.APIKeyID == nil {
		return "", false
	}
	if entry.ErrorOwner != "client" || !entry.IsBusinessLimited {
		return "", false
	}
	if entry.ErrorPhase != "request" && entry.ErrorPhase != "auth" {
		return "", false
	}
	if entry.StatusCode < 400 {
		return "", false
	}
	// An upstream was involved: the row carries provider attribution worth
	// keeping per occurrence.
	if entry.UpstreamStatusCode != nil || entry.UpstreamErrorMessage != nil ||
		entry.UpstreamErrorDetail != nil || len(entry.UpstreamErrors) > 0 {
		return "", false
	}
	bucketKey := strings.TrimSpace(code)
	if bucketKey == "" {
		bucketKey = strings.TrimSpace(entry.ErrorType)
	}
	if bucketKey == "" {
		// Without a stable code, distinct failures would share one bucket.
		return "", false
	}
	if !isOpsLocalBusinessLimitError(bucketKey, strings.ToLower(entry.ErrorMessage)) {
		return "", false
	}
	return strconv.FormatInt(*entry.APIKeyID, 10) + "\x00" + bucketKey, true
}

// opsCoalescedRepeatsPrefix marks an annotated row. Kept as a prefix so the
// original message stays a contiguous substring: error_message is searched with
// ILIKE and a trigram index, and classification matches it with strings.Contains.
const opsCoalescedRepeatsPrefix = "[repeated x"

func annotateOpsCoalescedRepeats(entry *service.OpsInsertErrorLogInput, suppressed int64) {
	if entry == nil || suppressed <= 0 {
		return
	}
	// +1 counts the occurrence that was written and opened the closed bucket.
	note := opsCoalescedRepeatsPrefix + strconv.FormatInt(suppressed+1, 10) + " in the preceding " +
		strconv.FormatInt(int64(opsCoalesceBucket/time.Second), 10) + "s] "
	entry.ErrorMessage = note + entry.ErrorMessage
}

func (c *opsCoalescer) reserveEntry() bool {
	for {
		current := c.tracked.Load()
		if current >= c.capacity {
			return false
		}
		if c.tracked.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

// maybeCleanupAtCapacity reclaims idle buckets one shard at a time, at most
// once per 100ms, and only once capacity is actually reached. Same shape as
// invalid_auth_abuse_limiter.maybeCleanupAtCapacity.
func (c *opsCoalescer) maybeCleanupAtCapacity(now time.Time) {
	if c.tracked.Load() < c.capacity {
		return
	}
	nowUnixNano := now.UnixNano()
	for {
		next := c.cleanupNext.Load()
		if nowUnixNano < next {
			return
		}
		if c.cleanupNext.CompareAndSwap(next, now.Add(100*time.Millisecond).UnixNano()) {
			break
		}
	}
	index := c.cleanupCursor.Add(1) - 1
	shard := &c.shards[index%opsCoalesceShardCount]
	shard.mu.Lock()
	for key, entry := range shard.entries {
		if c.entryIdle(entry, now) {
			delete(shard.entries, key)
			c.tracked.Add(-1)
		}
	}
	shard.mu.Unlock()
}

// entryIdle reports whether an entry can be dropped without losing a count:
// its bucket has closed and nothing is left to report. A bucket that only ever
// saw its one written occurrence qualifies — dropping it costs nothing, since a
// later occurrence would be written as a first occurrence either way.
func (c *opsCoalescer) entryIdle(entry *opsCoalesceEntry, now time.Time) bool {
	if entry == nil {
		return true
	}
	if now.Sub(entry.bucketStart) < c.bucket {
		return false
	}
	return entry.carried == 0 && entry.repeats == 0
}

func (c *opsCoalescer) shard(key string) *opsCoalesceShard {
	const fnvOffset32 = uint32(2166136261)
	const fnvPrime32 = uint32(16777619)
	hash := fnvOffset32
	for i := 0; i < len(key); i++ {
		hash ^= uint32(key[i])
		hash *= fnvPrime32
	}
	return &c.shards[hash%opsCoalesceShardCount]
}

func (c *opsCoalescer) health() OpsErrorLogCoalesceHealth {
	if c == nil {
		return OpsErrorLogCoalesceHealth{}
	}
	return OpsErrorLogCoalesceHealth{
		Tracked:      c.tracked.Load(),
		Capacity:     c.capacity,
		Admitted:     c.admitted.Load(),
		Suppressed:   c.suppressed.Load(),
		OverCapacity: c.overCapacity.Load(),
	}
}

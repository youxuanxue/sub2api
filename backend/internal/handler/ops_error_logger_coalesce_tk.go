package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
//     the next bucket's first row, so no occurrence is silently lost. A storm
//     may end before a next occurrence: a periodic body-free summary flushes
//     that tail, including at graceful shutdown and capacity reclamation.
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
	// Retain only bounded, body-free attribution for the final suppressed tail.
	// No request identifiers are copied into a summary row.
	summary *service.OpsInsertErrorLogInput
	ops     *service.OpsService
}

type opsCoalesceShard struct {
	mu      sync.Mutex
	entries map[string]*opsCoalesceEntry
}

type opsCoalescer struct {
	flushMu  sync.Mutex // serialize timer flush with the graceful-stop flush
	bucket   time.Duration
	capacity int64
	shards   [opsCoalesceShardCount]opsCoalesceShard
	now      func() time.Time

	tracked       atomic.Int64
	admitted      atomic.Uint64
	suppressed    atomic.Uint64
	dropped       atomic.Uint64
	overCapacity  atomic.Uint64
	cleanupNext   atomic.Int64
	cleanupCursor atomic.Uint32
}

// OpsErrorLogCoalesceHealth exposes the gate's counters for ops visibility.
// suppressed counts original rows coalesced, whether carried, pending, or
// flushed as a tail summary. dropped counts reclaimed residues without a sink;
// queue/database delivery failures are tracked by the existing worker health.
type OpsErrorLogCoalesceHealth struct {
	Tracked      int64  `json:"tracked"`
	Capacity     int64  `json:"capacity"`
	Admitted     uint64 `json:"admitted"`
	Suppressed   uint64 `json:"suppressed"`
	Dropped      uint64 `json:"dropped"`
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
func maybeCoalesceOpsErrorLogEntry(ops *service.OpsService, entry *service.OpsInsertErrorLogInput, code string) bool {
	return opsErrorLogCoalescer.admitForService(ops, entry, code)
}

func (c *opsCoalescer) admit(entry *service.OpsInsertErrorLogInput, code string) bool {
	return c.admitForService(nil, entry, code)
}

func (c *opsCoalescer) admitForService(ops *service.OpsService, entry *service.OpsInsertErrorLogInput, code string) bool {
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
	if ops != nil {
		existing.ops = ops
		existing.summary = opsCoalescedSummary(entry, now)
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
	// Keep the attribution stable within a bucket: a model/group/status change
	// or count_tokens probe on the same key must not borrow another's count.
	// Hash the bounded summary so dimension cardinality cannot retain large text.
	payload, err := json.Marshal(struct {
		Code        string
		Attribution *service.OpsInsertErrorLogInput
	}{bucketKey, opsCoalescedSummary(entry, time.Time{})})
	if err != nil {
		return "", false // fail open; never suppress an unrepresentable identity
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), true
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

// maybeCleanupAtCapacity reclaims idle buckets once capacity is reached. Same
// shape as invalid_auth_abuse_limiter.maybeCleanupAtCapacity.
func (c *opsCoalescer) maybeCleanupAtCapacity(now time.Time) {
	if c.tracked.Load() < c.capacity {
		return
	}
	c.reclaimIdle(now)
}

// reclaimIdle reclaims closed buckets from one shard, at most once per 100ms.
//
// An entry is reclaimed once its bucket has closed, whether or not it still
// holds a suppressed count. Requiring a zero count was a defect: the residue of
// a *finished* storm is exactly a non-zero count with no further occurrence
// coming to carry it out, so such entries were never reclaimable. A fleet-wide
// churn of briefly-rejected keys could then pin the map at capacity and leave
// the gate permanently failed open — coalescing silently stops working.
//
// A production residue is queued as a summary. Without a service sink (isolated
// coalescer users), it is accounted for in dropped rather than silently lost.
func (c *opsCoalescer) reclaimIdle(now time.Time) {
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

	var residue int64
	var jobs []opsErrorLogJob
	var reclaimed int64
	shard.mu.Lock()
	for key, entry := range shard.entries {
		if !c.entryIdle(entry, now) {
			continue
		}
		if job, ok := opsCoalescedTail(entry); ok {
			jobs = append(jobs, job)
		} else {
			residue += entry.carried + entry.repeats
		}
		delete(shard.entries, key)
		reclaimed++
	}
	shard.mu.Unlock()

	for _, job := range jobs {
		enqueueOpsErrorLog(job.ops, job.entry)
	}
	if reclaimed > 0 {
		c.tracked.Add(-reclaimed)
	}
	if residue > 0 {
		c.dropped.Add(uint64(residue))
	}
}

// entryIdle reports whether an entry's bucket has closed, meaning a later
// occurrence would be written as a first occurrence anyway and the entry no
// longer affects any decision. A held count does not keep an entry alive — see
// reclaimIdle for why that would be a leak rather than a safeguard.
func (c *opsCoalescer) entryIdle(entry *opsCoalesceEntry, now time.Time) bool {
	if entry == nil {
		return true
	}
	return now.Sub(entry.bucketStart) >= c.bucket
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
		Dropped:      c.dropped.Load(),
		OverCapacity: c.overCapacity.Load(),
	}
}

// opsCoalescedSummary intentionally omits bodies, client IP/UA, key prefixes and
// request/trajectory IDs. A summary represents several requests, never a replay.
func opsCoalescedSummary(entry *service.OpsInsertErrorLogInput, now time.Time) *service.OpsInsertErrorLogInput {
	bounded := func(s string, max int) string { return strings.Clone(truncateString(s, max)) }
	return &service.OpsInsertErrorLogInput{
		UserID: entry.UserID, APIKeyID: entry.APIKeyID, GroupID: entry.GroupID, AccountID: entry.AccountID,
		Platform: bounded(entry.Platform, 64), Model: bounded(entry.Model, 256),
		RequestedModel: bounded(entry.RequestedModel, 256),
		RequestPath:    bounded(entry.RequestPath, 256), InboundEndpoint: bounded(entry.InboundEndpoint, 256),
		Stream: entry.Stream, RequestType: entry.RequestType,
		ErrorPhase: entry.ErrorPhase, ErrorType: entry.ErrorType, Severity: entry.Severity,
		StatusCode: entry.StatusCode, IsBusinessLimited: entry.IsBusinessLimited, IsCountTokens: entry.IsCountTokens,
		ErrorOwner: entry.ErrorOwner, ErrorSource: entry.ErrorSource,
		ErrorMessage: bounded(entry.ErrorMessage, 512), CreatedAt: now,
	}
}

func opsCoalescedTail(entry *opsCoalesceEntry) (opsErrorLogJob, bool) {
	count := entry.carried + entry.repeats
	if count == 0 || entry.ops == nil || entry.summary == nil {
		return opsErrorLogJob{}, false
	}
	summary := *entry.summary
	// Only suppressed requests: the bucket's first row is already persisted.
	// count=1 needs no prefix; larger tails use the same count contract as carry.
	annotateOpsCoalescedRepeats(&summary, count-1)
	return opsErrorLogJob{ops: entry.ops, entry: &summary}, true
}

// flushClosed writes the tail even when the client stops retrying. Normal flush
// happens once a minute after bucket close; graceful shutdown also flushes open
// buckets. Rows retain the last occurrence time, not the flush time, so idle
// clients never reappear as a new incident. Locks arbitrate flush vs next admit.
func (c *opsCoalescer) flushClosed(all bool) {
	if c == nil {
		return
	}
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	now := c.now()
	for i := range c.shards {
		shard := &c.shards[i]
		var jobs []opsErrorLogJob
		shard.mu.Lock()
		for key, entry := range shard.entries {
			if !all && !c.entryIdle(entry, now) {
				continue
			}
			if job, ok := opsCoalescedTail(entry); ok {
				jobs = append(jobs, job)
			} else if entry.carried+entry.repeats > 0 {
				c.dropped.Add(uint64(entry.carried + entry.repeats))
			}
			delete(shard.entries, key)
			c.tracked.Add(-1)
		}
		shard.mu.Unlock()
		for _, job := range jobs {
			enqueueOpsErrorLog(job.ops, job.entry)
		}
	}
}

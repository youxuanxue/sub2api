package handler

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// quotaRejectEntry builds the row shape that the measured retry storm produced:
// a client-owned, business-limited 429 on a known key with no upstream contact.
func quotaRejectEntry(apiKeyID int64) *service.OpsInsertErrorLogInput {
	id := apiKeyID
	return &service.OpsInsertErrorLogInput{
		APIKeyID:          &id,
		ErrorOwner:        "client",
		ErrorPhase:        "request",
		ErrorType:         "rate_limit_error",
		StatusCode:        429,
		IsBusinessLimited: true,
		ErrorMessage:      "API key 额度已用完",
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestCoalescer(capacity int64) (*opsCoalescer, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return newOpsCoalescer(time.Minute, capacity, clock.Now), clock
}

// TestOpsCoalesceWritesFirstOccurrenceImmediately is the signal guarantee: the
// very first row is never delayed or batched away.
func TestOpsCoalesceWritesFirstOccurrenceImmediately(t *testing.T) {
	c, _ := newTestCoalescer(opsCoalesceCapacity)

	entry := quotaRejectEntry(362)
	require.True(t, c.admit(entry, opsCodeAPIKeyQuotaExhausted))
	require.Equal(t, "API key 额度已用完", entry.ErrorMessage, "first row is written unannotated")
	require.EqualValues(t, 1, c.health().Admitted)
	require.EqualValues(t, 0, c.health().Suppressed)
}

// TestOpsCoalesceCollapsesRepeatsWithinBucket reproduces the storm: one minute
// of ~356 identical rejections must leave exactly one row.
func TestOpsCoalesceCollapsesRepeatsWithinBucket(t *testing.T) {
	c, clock := newTestCoalescer(opsCoalesceCapacity)

	written := 0
	for i := 0; i < 356; i++ {
		if c.admit(quotaRejectEntry(362), opsCodeAPIKeyQuotaExhausted) {
			written++
		}
		clock.Advance(168 * time.Millisecond) // ~356/min, still inside one bucket
	}

	require.Equal(t, 1, written, "356 identical rejections in one bucket must write one row")
	require.EqualValues(t, 355, c.health().Suppressed)
}

// TestOpsCoalesceCarriesSuppressedCountOntoNextRow proves no occurrence is
// silently lost: the next bucket's row reports everything the last one dropped.
func TestOpsCoalesceCarriesSuppressedCountOntoNextRow(t *testing.T) {
	c, clock := newTestCoalescer(opsCoalesceCapacity)

	require.True(t, c.admit(quotaRejectEntry(362), opsCodeAPIKeyQuotaExhausted))
	for i := 0; i < 99; i++ {
		require.False(t, c.admit(quotaRejectEntry(362), opsCodeAPIKeyQuotaExhausted))
	}

	clock.Advance(time.Minute)
	next := quotaRejectEntry(362)
	require.True(t, c.admit(next, opsCodeAPIKeyQuotaExhausted))
	// 100 occurrences happened in the closed bucket: 1 written + 99 dropped.
	require.Equal(t, "[repeated x100 in the preceding 60s] API key 额度已用完", next.ErrorMessage)

	// A fresh bucket with no repeats reports nothing.
	clock.Advance(time.Minute)
	clean := quotaRejectEntry(362)
	require.True(t, c.admit(clean, opsCodeAPIKeyQuotaExhausted))
	require.Equal(t, "API key 额度已用完", clean.ErrorMessage)
}

// TestOpsCoalesceNeverSuppressesDifferentKeyOrCode is the isolation guarantee:
// one key's storm must not hide anyone else's failure.
func TestOpsCoalesceNeverSuppressesDifferentKeyOrCode(t *testing.T) {
	c, _ := newTestCoalescer(opsCoalesceCapacity)

	// Saturate key 362's quota bucket.
	require.True(t, c.admit(quotaRejectEntry(362), opsCodeAPIKeyQuotaExhausted))
	for i := 0; i < 500; i++ {
		require.False(t, c.admit(quotaRejectEntry(362), opsCodeAPIKeyQuotaExhausted))
	}

	// A different key, same code: written.
	other := quotaRejectEntry(999)
	require.True(t, c.admit(other, opsCodeAPIKeyQuotaExhausted), "another key must not be suppressed")
	require.Equal(t, "API key 额度已用完", other.ErrorMessage)

	// Same key, a different code: written.
	differentCode := quotaRejectEntry(362)
	differentCode.ErrorMessage = "Insufficient account balance"
	require.True(t, c.admit(differentCode, opsCodeInsufficientBalance), "another code on the same key must not be suppressed")

	// And each of those now has its own independent bucket.
	require.False(t, c.admit(quotaRejectEntry(999), opsCodeAPIKeyQuotaExhausted))
	require.EqualValues(t, 3, c.health().Admitted)
}

// TestOpsCoalesceSkipsRowsThatCarryInformation keeps the gate narrow: anything
// that is not a client-owned local limit rejection is written every time.
func TestOpsCoalesceSkipsRowsThatCarryInformation(t *testing.T) {
	upstreamStatus := 503
	upstreamMessage := "upstream exploded"

	cases := []struct {
		name   string
		code   string
		mutate func(*service.OpsInsertErrorLogInput)
	}{
		{"no api key", opsCodeAPIKeyQuotaExhausted, func(e *service.OpsInsertErrorLogInput) { e.APIKeyID = nil }},
		{"provider owned", opsCodeAPIKeyQuotaExhausted, func(e *service.OpsInsertErrorLogInput) { e.ErrorOwner = "provider" }},
		{"platform owned", opsCodeAPIKeyQuotaExhausted, func(e *service.OpsInsertErrorLogInput) { e.ErrorOwner = "platform" }},
		{"not business limited", opsCodeAPIKeyQuotaExhausted, func(e *service.OpsInsertErrorLogInput) { e.IsBusinessLimited = false }},
		{"routing phase", opsCodeAPIKeyQuotaExhausted, func(e *service.OpsInsertErrorLogInput) { e.ErrorPhase = "routing" }},
		{"upstream phase", opsCodeAPIKeyQuotaExhausted, func(e *service.OpsInsertErrorLogInput) { e.ErrorPhase = "upstream" }},
		{"upstream status present", opsCodeAPIKeyQuotaExhausted, func(e *service.OpsInsertErrorLogInput) { e.UpstreamStatusCode = &upstreamStatus }},
		{"upstream message present", opsCodeAPIKeyQuotaExhausted, func(e *service.OpsInsertErrorLogInput) { e.UpstreamErrorMessage = &upstreamMessage }},
		{"upstream events present", opsCodeAPIKeyQuotaExhausted, func(e *service.OpsInsertErrorLogInput) {
			e.UpstreamErrors = []*service.OpsUpstreamErrorEvent{{}}
		}},
		{"non-error status", opsCodeAPIKeyQuotaExhausted, func(e *service.OpsInsertErrorLogInput) { e.StatusCode = 200 }},
		{"internal error is not a client limit", "INTERNAL_ERROR", func(e *service.OpsInsertErrorLogInput) {
			e.ErrorMessage = "Failed to validate API key"
		}},
		{"no stable code", "", func(e *service.OpsInsertErrorLogInput) { e.ErrorType = "" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestCoalescer(opsCoalesceCapacity)
			for i := 0; i < 5; i++ {
				entry := quotaRejectEntry(362)
				tc.mutate(entry)
				require.True(t, c.admit(entry, tc.code), "occurrence %d must be written", i)
				require.NotContains(t, entry.ErrorMessage, opsCoalescedRepeatsPrefix)
			}
			require.EqualValues(t, 0, c.health().Suppressed)
		})
	}
}

// TestOpsCoalesceFailsOpenAtCapacity: memory pressure must cost rows written,
// never signal suppressed.
func TestOpsCoalesceFailsOpenAtCapacity(t *testing.T) {
	c, _ := newTestCoalescer(2)

	require.True(t, c.admit(quotaRejectEntry(1), opsCodeAPIKeyQuotaExhausted))
	require.True(t, c.admit(quotaRejectEntry(2), opsCodeAPIKeyQuotaExhausted))
	require.EqualValues(t, 2, c.health().Tracked)

	// Capacity reached: an untracked bucket is written rather than coalesced,
	// repeatedly.
	for i := 0; i < 3; i++ {
		require.True(t, c.admit(quotaRejectEntry(3), opsCodeAPIKeyQuotaExhausted))
	}
	require.EqualValues(t, 3, c.health().OverCapacity)
	require.EqualValues(t, 0, c.health().Suppressed)

	// Tracked keys keep coalescing.
	require.False(t, c.admit(quotaRejectEntry(1), opsCodeAPIKeyQuotaExhausted))
}

// TestOpsCoalesceReclaimsIdleBucketsAtCapacity proves the map does not grow
// without bound as keys come and go.
func TestOpsCoalesceReclaimsIdleBucketsAtCapacity(t *testing.T) {
	const capacity = 64
	c, clock := newTestCoalescer(capacity)

	for i := int64(0); i < capacity; i++ {
		require.True(t, c.admit(quotaRejectEntry(i), opsCodeAPIKeyQuotaExhausted))
	}
	require.EqualValues(t, capacity, c.health().Tracked)

	// All buckets go idle, then a long stream of fresh keys arrives — far more
	// than capacity. Cleanup sweeps one shard per 100ms tick, so advance the
	// clock between arrivals to let it run.
	clock.Advance(2 * time.Minute)
	const fresh = 20 * capacity
	for i := int64(0); i < fresh; i++ {
		require.True(t, c.admit(quotaRejectEntry(capacity+i), opsCodeAPIKeyQuotaExhausted))
		clock.Advance(150 * time.Millisecond)
	}

	// 20x capacity distinct keys passed through and the map never grew past
	// capacity, so idle buckets were genuinely reclaimed rather than leaked.
	require.LessOrEqual(t, c.health().Tracked, int64(capacity), "tracked buckets must stay bounded")
	require.Greater(t, c.health().Tracked, int64(0))
	// Arrivals that found the table full were written, never suppressed.
	require.EqualValues(t, capacity+fresh, c.health().Admitted)
	require.EqualValues(t, 0, c.health().Suppressed)
}

// TestOpsCoalesceKeepsCoalescingStormUnderKeyChurn is the property that matters
// under capacity pressure: a continuously storming key stays coalesced even
// while thousands of one-off keys churn through the table. An active bucket is
// never idle, so reclamation cannot evict it, and low-volume keys fail open
// into written rows — pressure costs rows, never signal.
func TestOpsCoalesceKeepsCoalescingStormUnderKeyChurn(t *testing.T) {
	const capacity = 32
	c, clock := newTestCoalescer(capacity)

	const stormKey = int64(362)
	stormWritten := 0
	for i := int64(0); i < 40*capacity; i++ {
		if c.admit(quotaRejectEntry(stormKey), opsCodeAPIKeyQuotaExhausted) {
			stormWritten++
		}
		// A distinct one-off key between each storm hit.
		c.admit(quotaRejectEntry(1_000_000+i), opsCodeAPIKeyQuotaExhausted)
		clock.Advance(50 * time.Millisecond)
	}

	// 1280 storm hits spanning 64s, i.e. two buckets: at most one row each.
	require.LessOrEqual(t, stormWritten, 2, "storm key must stay coalesced under churn")
	require.LessOrEqual(t, c.health().Tracked, int64(capacity))
}

// TestOpsCoalesceAnnotationStaysSearchable: error_message is searched with
// ILIKE and matched with strings.Contains, so the original text must survive
// annotation intact and classification must not shift.
func TestOpsCoalesceAnnotationStaysSearchable(t *testing.T) {
	entry := quotaRejectEntry(362)
	annotateOpsCoalescedRepeats(entry, 41)

	require.Contains(t, entry.ErrorMessage, "API key 额度已用完")
	require.True(t, isOpsLocalBusinessLimitError(opsCodeAPIKeyQuotaExhausted, entry.ErrorMessage),
		"annotated message is still classified as a local business limit")
	require.True(t, classifyOpsIsBusinessLimited("rate_limit_error", "request", opsCodeAPIKeyQuotaExhausted, 429, entry.ErrorMessage))
}

// TestOpsCoalesceConcurrentAdmitsLoseNoOccurrence: under parallel load the
// written rows plus the suppressed count must equal the occurrences offered.
func TestOpsCoalesceConcurrentAdmitsLoseNoOccurrence(t *testing.T) {
	c, _ := newTestCoalescer(opsCoalesceCapacity)

	const workers = 16
	const perWorker = 200
	var written atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// Four distinct keys so several buckets are contended at once.
			keyID := int64(w % 4)
			for i := 0; i < perWorker; i++ {
				if c.admit(quotaRejectEntry(keyID), opsCodeAPIKeyQuotaExhausted) {
					written.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()

	health := c.health()
	require.EqualValues(t, workers*perWorker, written.Load()+int64(health.Suppressed),
		"every offered occurrence is either written or counted")
	require.EqualValues(t, written.Load(), int64(health.Admitted))
	// Four keys, one bucket each, no clock advance: exactly four rows.
	require.EqualValues(t, 4, written.Load())
}

// TestOpsCoalesceMeasuredStormReduction quantifies the fix against the measured
// prod shape: 90 minutes at ~356 req/min from one key.
func TestOpsCoalesceMeasuredStormReduction(t *testing.T) {
	c, clock := newTestCoalescer(opsCoalesceCapacity)

	const minutes = 90
	const perMinute = 356
	written := 0
	for m := 0; m < minutes; m++ {
		for i := 0; i < perMinute; i++ {
			if c.admit(quotaRejectEntry(362), opsCodeAPIKeyQuotaExhausted) {
				written++
			}
			clock.Advance(time.Minute / perMinute)
		}
	}

	require.Equal(t, minutes, written, "one row per minute, not "+strconv.Itoa(minutes*perMinute))
	require.EqualValues(t, minutes*perMinute-minutes, c.health().Suppressed)
}

// A storm always ends. When it does, the final bucket's suppressed count has no
// next occurrence to ride out on, and the entry still holds a non-zero count —
// which, before the dropped-counter fix, made it permanently unreclaimable.
// Both halves are regressions worth pinning: the count must be accounted for,
// and the entry must be reclaimable.
func TestOpsCoalesceStormEndAccountsForTrailingRepeats(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1700000000, 0)}
	c := newOpsCoalescer(time.Minute, 4096, clock.Now)

	for i := 0; i < 10; i++ {
		require.Equal(t, i == 0, c.admit(quotaRejectEntry(362), "API_KEY_QUOTA_EXHAUSTED"),
			"only the first occurrence of the bucket is written")
	}
	require.Equal(t, uint64(9), c.health().Suppressed)

	// The client stops. Nothing will ever admit this key again.
	clock.Advance(10 * time.Minute)
	for i := 0; i < 64; i++ {
		c.reclaimIdle(clock.Now())
		clock.Advance(200 * time.Millisecond)
	}

	h := c.health()
	require.Zero(t, h.Tracked, "the entry must be reclaimed once its bucket closed")
	require.Equal(t, uint64(9), h.Dropped,
		"occurrences that never made it onto a row must be accounted for, not silently lost")
}

// The failure mode this guards: entries carrying a suppressed count were exempt
// from reclaim, so a fleet-wide churn of briefly-rejected keys could pin the map
// at capacity and leave the gate permanently failed open — coalescing silently
// stops working, with only an over_capacity counter nobody reads to show it.
func TestOpsCoalesceRecoversFromCapacityWithTrailingRepeats(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1700000000, 0)}
	const capacity = int64(64)
	c := newOpsCoalescer(time.Minute, capacity, clock.Now)

	for key := int64(0); key < capacity; key++ {
		for i := 0; i < 3; i++ { // one written, two suppressed
			c.admit(quotaRejectEntry(key), "API_KEY_QUOTA_EXHAUSTED")
		}
	}
	require.Equal(t, capacity, c.health().Tracked)

	clock.Advance(10 * time.Minute)
	for i := 0; i < 512; i++ {
		c.reclaimIdle(clock.Now())
		clock.Advance(200 * time.Millisecond)
	}

	h := c.health()
	require.Zero(t, h.Tracked, "idle entries must be reclaimable even when they carry a count")
	require.Equal(t, uint64(capacity*2), h.Dropped, "every suppressed occurrence is accounted for")

	// The gate works again rather than staying failed open.
	require.True(t, c.admit(quotaRejectEntry(9999), "API_KEY_QUOTA_EXHAUSTED"))
	require.False(t, c.admit(quotaRejectEntry(9999), "API_KEY_QUOTA_EXHAUSTED"),
		"coalescing is live again, not permanently failed open")
}

func TestOpsCoalesceSeparatesAlertAttribution(t *testing.T) {
	c, _ := newTestCoalescer(opsCoalesceCapacity)
	first := quotaRejectEntry(362)
	first.Model = "qwen3.8-max"
	require.True(t, c.admit(first, opsCodeAPIKeyQuotaExhausted))
	for _, change := range []func(*service.OpsInsertErrorLogInput){
		func(e *service.OpsInsertErrorLogInput) { e.Model = "kimi-k3" },
		func(e *service.OpsInsertErrorLogInput) { e.IsCountTokens = true },
		func(e *service.OpsInsertErrorLogInput) { e.StatusCode = 403 },
		func(e *service.OpsInsertErrorLogInput) { id := int64(99); e.GroupID = &id },
	} {
		entry := *first
		change(&entry)
		require.True(t, c.admit(&entry, opsCodeAPIKeyQuotaExhausted))
		require.False(t, c.admit(&entry, opsCodeAPIKeyQuotaExhausted))
	}
	require.False(t, c.admit(first, opsCodeAPIKeyQuotaExhausted))
}

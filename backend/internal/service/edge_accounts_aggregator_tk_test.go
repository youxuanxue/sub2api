//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type edgeAccountsStoreStub struct {
	accounts []Account
	err      error
}

func (s *edgeAccountsStoreStub) ListByPlatform(_ context.Context, _ string) ([]Account, error) {
	return s.accounts, s.err
}

// fakeEdgeDoer routes by request host, records the x-api-key seen per host, counts
// total calls (for cache-hit assertions), and returns a canned response or error.
// The whole body runs under the mutex so it is race-safe under the SWR background
// refresh (which calls Do concurrently with the test goroutine).
type fakeEdgeDoer struct {
	mu           sync.Mutex
	bodyByHost   map[string]string // host -> JSON body for a 200
	errByHost    map[string]error  // host -> transport error
	statusByHost map[string]int    // host -> non-200 status
	keysSeen     map[string]string // host -> x-api-key
	calls        atomic.Int64      // total Do invocations across all fan-outs
}

func (f *fakeEdgeDoer) Do(req *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	host := req.URL.Host
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keysSeen == nil {
		f.keysSeen = map[string]string{}
	}
	f.keysSeen[host] = req.Header.Get("x-api-key")

	if err := f.errByHost[host]; err != nil {
		return nil, err
	}
	status := http.StatusOK
	if s, ok := f.statusByHost[host]; ok {
		status = s
	}
	body := f.bodyByHost[host]
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func mirrorStub(id int64, baseURL, apiKey string) Account {
	// A normal active mirror stub is schedulable (prod routes to its edge); the
	// "关调度" cases flip Schedulable to false explicitly.
	return Account{
		ID:          id,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"base_url": baseURL,
			"api_key":  apiKey,
		},
	}
}

func TestEdgeAccountsAggregator_FanoutDedupAndErrorIsolation(t *testing.T) {
	store := &edgeAccountsStoreStub{accounts: []Account{
		mirrorStub(1, "https://api-us1.tokenkey.dev", "key-us1"),
		mirrorStub(2, "https://api-fra1.tokenkey.dev", "key-fra1"),
		// duplicate of us1 (trailing slash) — must be deduped, first kept
		mirrorStub(3, "https://api-us1.tokenkey.dev/", "key-us1-dup"),
		// non-stub: oauth account, must be ignored
		{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
			Credentials: map[string]any{"base_url": "https://api-sg1.tokenkey.dev"}},
		// non-stub: base_url not an internal edge
		mirrorStub(5, "https://example.com", "key-x"),
	}}

	doer := &fakeEdgeDoer{
		bodyByHost: map[string]string{
			"api-us1.tokenkey.dev": `{"code":0,"data":{"platform":"anthropic","accounts":[{"id":11,"name":"a","platform":"anthropic","type":"apikey","status":"active"}]}}`,
		},
		errByHost: map[string]error{
			"api-fra1.tokenkey.dev": errors.New("connection timeout"),
		},
	}

	agg := NewEdgeAccountsAggregator(store, doer)
	out, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)
	require.Equal(t, "anthropic", out.Platform)
	require.NotZero(t, out.TS)

	// Exactly two unique edges (us1, fra1); sorted by edge_id → fra1 first.
	require.Len(t, out.Edges, 2)
	require.Equal(t, "fra1", out.Edges[0].EdgeID)
	require.Equal(t, "us1", out.Edges[1].EdgeID)

	// fra1: transport error → ok:false, isolated (aggregate still succeeds). The
	// stub's scheduling toggle is reported regardless of reachability.
	require.False(t, out.Edges[0].OK)
	require.Contains(t, out.Edges[0].Error, "request failed")
	require.Empty(t, out.Edges[0].Accounts)
	require.True(t, out.Edges[0].StubSchedulable)

	// us1: ok with one account; the FIRST stub's api_key is used (dedup kept first).
	// Accounts are opaque json.RawMessage passthrough — assert on the raw JSON.
	require.True(t, out.Edges[1].OK)
	require.True(t, out.Edges[1].StubSchedulable)
	require.Len(t, out.Edges[1].Accounts, 1)
	require.Contains(t, string(out.Edges[1].Accounts[0]), `"id":11`)

	require.Equal(t, "key-us1", doer.keysSeen["api-us1.tokenkey.dev"])
	require.Equal(t, "key-fra1", doer.keysSeen["api-fra1.tokenkey.dev"])
	// sg1 (oauth) and example.com (non-edge) must never have been called.
	require.NotContains(t, doer.keysSeen, "api-sg1.tokenkey.dev")
	require.NotContains(t, doer.keysSeen, "example.com")
}

func TestEdgeAccountsAggregator_SkipsDisabledStub(t *testing.T) {
	disabled := mirrorStub(1, "https://api-fra1.tokenkey.dev", "key-fra1")
	disabled.Status = StatusDisabled
	// us6 has a disabled stub listed before an active one for the same base_url:
	// the disabled one is skipped, the active one still covers the edge.
	us6Disabled := mirrorStub(2, "https://api-us6.tokenkey.dev", "key-us6-old")
	us6Disabled.Status = StatusDisabled
	us6Active := mirrorStub(3, "https://api-us6.tokenkey.dev", "key-us6")
	us6Active.Status = StatusActive
	// us7's only stub is in 'error' (transient) — must NOT be skipped.
	us7Err := mirrorStub(4, "https://api-us7.tokenkey.dev", "key-us7")
	us7Err.Status = "error"

	store := &edgeAccountsStoreStub{accounts: []Account{disabled, us6Disabled, us6Active, us7Err}}
	doer := &fakeEdgeDoer{bodyByHost: map[string]string{
		"api-us6.tokenkey.dev": `{"code":0,"data":{"accounts":[]}}`,
		"api-us7.tokenkey.dev": `{"code":0,"data":{"accounts":[]}}`,
	}}

	agg := NewEdgeAccountsAggregator(store, doer)
	out, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)

	// fra1 (disabled-only) dropped entirely; us6 + us7 remain (sorted).
	require.Len(t, out.Edges, 2)
	require.Equal(t, "us6", out.Edges[0].EdgeID)
	require.Equal(t, "us7", out.Edges[1].EdgeID)

	// Disabled fra1 was never polled.
	require.NotContains(t, doer.keysSeen, "api-fra1.tokenkey.dev")
	// us6 used the ACTIVE stub's key (the disabled one ahead of it was skipped).
	require.Equal(t, "key-us6", doer.keysSeen["api-us6.tokenkey.dev"])
	// us7 in 'error' state is still reachable and was polled.
	require.Equal(t, "key-us7", doer.keysSeen["api-us7.tokenkey.dev"])
}

// TestEdgeAccountsAggregator_PausedStubStillListedButFlagged covers the operator
// "关调度" case: the prod-side stub for an edge has scheduling turned off but is
// still active. Unlike a fully disabled stub (dropped from the aggregate), a
// paused stub's edge is still reachable, so it must STILL be polled and listed —
// just flagged StubSchedulable=false so the overview shows prod has stopped
// routing there. This is the gap the fix closes: before, the paused state was
// invisible on the cross-edge overview.
func TestEdgeAccountsAggregator_PausedStubStillListedButFlagged(t *testing.T) {
	paused := mirrorStub(1, "https://api-us4.tokenkey.dev", "key-us4")
	paused.Schedulable = false // 关调度: active but taken out of prod rotation
	active := mirrorStub(2, "https://api-us1.tokenkey.dev", "key-us1")

	store := &edgeAccountsStoreStub{accounts: []Account{paused, active}}
	doer := &fakeEdgeDoer{bodyByHost: map[string]string{
		"api-us4.tokenkey.dev": `{"code":0,"data":{"accounts":[{"id":41,"name":"x"}]}}`,
		"api-us1.tokenkey.dev": `{"code":0,"data":{"accounts":[]}}`,
	}}

	agg := NewEdgeAccountsAggregator(store, doer)
	out, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)

	// Both edges present; the schedulable edge surfaces first and the paused one
	// sinks to the bottom (here us1 < us4 by edge_id too — see the dedicated sink
	// test below for the case where the paused edge has the smaller edge_id).
	require.Len(t, out.Edges, 2)
	require.Equal(t, "us1", out.Edges[0].EdgeID)
	require.Equal(t, "us4", out.Edges[1].EdgeID)

	// us4 was still polled (reachable) and its accounts listed — the pause does
	// not hide the edge, only flags it.
	require.Equal(t, "key-us4", doer.keysSeen["api-us4.tokenkey.dev"])
	require.True(t, out.Edges[1].OK)
	require.Len(t, out.Edges[1].Accounts, 1)

	// The pause is now visible: us4 flagged not-schedulable, us1 schedulable.
	require.False(t, out.Edges[1].StubSchedulable)
	require.True(t, out.Edges[0].StubSchedulable)
}

// TestEdgeAccountsAggregator_PausedStubsSinkBelowEdgeID proves the ordering is
// driven by scheduling state FIRST, edge_id only as a within-band tiebreak: a
// paused edge sinks to the bottom even when its edge_id sorts ahead of the live
// edges. Here fra1 (paused) is lexically smaller than sg1/us1 (schedulable), so a
// plain edge_id sort would have put fra1 first; the fix sinks it last instead so
// the operator's in-rotation edges always surface at the top of the overview.
func TestEdgeAccountsAggregator_PausedStubsSinkBelowEdgeID(t *testing.T) {
	fra1 := mirrorStub(1, "https://api-fra1.tokenkey.dev", "key-fra1")
	fra1.Schedulable = false // 关调度, but smallest edge_id
	us1 := mirrorStub(2, "https://api-us1.tokenkey.dev", "key-us1")
	sg1 := mirrorStub(3, "https://api-sg1.tokenkey.dev", "key-sg1")

	store := &edgeAccountsStoreStub{accounts: []Account{fra1, us1, sg1}}
	doer := &fakeEdgeDoer{bodyByHost: map[string]string{
		"api-fra1.tokenkey.dev": `{"code":0,"data":{"accounts":[]}}`,
		"api-us1.tokenkey.dev":  `{"code":0,"data":{"accounts":[]}}`,
		"api-sg1.tokenkey.dev":  `{"code":0,"data":{"accounts":[]}}`,
	}}

	agg := NewEdgeAccountsAggregator(store, doer)
	out, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)

	// Schedulable band first (sg1 < us1 by edge_id), paused fra1 sinks last
	// despite having the smallest edge_id of the three.
	require.Len(t, out.Edges, 3)
	require.Equal(t, "sg1", out.Edges[0].EdgeID)
	require.True(t, out.Edges[0].StubSchedulable)
	require.Equal(t, "us1", out.Edges[1].EdgeID)
	require.True(t, out.Edges[1].StubSchedulable)
	require.Equal(t, "fra1", out.Edges[2].EdgeID)
	require.False(t, out.Edges[2].StubSchedulable)
}

func TestEdgeAccountsAggregator_Non2xxIsError(t *testing.T) {
	store := &edgeAccountsStoreStub{accounts: []Account{
		mirrorStub(1, "https://api-us1.tokenkey.dev", "key-us1"),
	}}
	doer := &fakeEdgeDoer{statusByHost: map[string]int{"api-us1.tokenkey.dev": http.StatusBadGateway}}
	agg := NewEdgeAccountsAggregator(store, doer)
	out, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)
	require.Len(t, out.Edges, 1)
	require.False(t, out.Edges[0].OK)
	require.Contains(t, out.Edges[0].Error, "http 502")
}

func TestEdgeAccountsAggregator_NoMirrorStubs(t *testing.T) {
	store := &edgeAccountsStoreStub{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	}}
	agg := NewEdgeAccountsAggregator(store, &fakeEdgeDoer{})
	out, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)
	require.Empty(t, out.Edges)
}

func TestEdgeAccountsAggregator_ListError(t *testing.T) {
	store := &edgeAccountsStoreStub{err: errors.New("db down")}
	agg := NewEdgeAccountsAggregator(store, &fakeEdgeDoer{})
	_, err := agg.Aggregate(context.Background(), "anthropic")
	require.Error(t, err)
}

// testClock is an injectable, race-safe clock for driving the soft-TTL window.
type testClock struct{ ns atomic.Int64 }

func newTestClock() *testClock {
	c := &testClock{}
	c.ns.Store(time.Unix(1_700_000_000, 0).UnixNano())
	return c
}
func (c *testClock) now() time.Time          { return time.Unix(0, c.ns.Load()) }
func (c *testClock) advance(d time.Duration) { c.ns.Add(int64(d)) }

// Within edgeAccountsSoftTTL a second Aggregate is served from cache — no second
// fan-out. This is the core "太慢" fix: repeated loads/refreshes don't re-fan-out.
func TestEdgeAccountsAggregator_CacheServesWithinSoftTTL(t *testing.T) {
	store := &edgeAccountsStoreStub{accounts: []Account{
		mirrorStub(1, "https://api-us1.tokenkey.dev", "key-us1"),
	}}
	doer := &fakeEdgeDoer{bodyByHost: map[string]string{
		"api-us1.tokenkey.dev": `{"code":0,"data":{"accounts":[]}}`,
	}}
	clk := newTestClock()
	agg := NewEdgeAccountsAggregator(store, doer)
	agg.now = clk.now

	out1, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)
	require.Len(t, out1.Edges, 1)
	require.Equal(t, int64(1), doer.calls.Load(), "cold load fans out once")

	clk.advance(edgeAccountsSoftTTL / 2) // still fresh
	out2, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)
	require.Len(t, out2.Edges, 1)
	require.Equal(t, int64(1), doer.calls.Load(), "fresh hit must not re-fan-out")
}

// Past the soft TTL, Aggregate returns the cached (stale) aggregate immediately and
// refreshes in the background; the next call (now fresh again) serves the refreshed
// data. A slow/dead edge therefore only ever costs the background goroutine.
func TestEdgeAccountsAggregator_StaleServesCachedThenBackgroundRefresh(t *testing.T) {
	store := &edgeAccountsStoreStub{accounts: []Account{
		mirrorStub(1, "https://api-us1.tokenkey.dev", "key-us1"),
	}}
	doer := &fakeEdgeDoer{bodyByHost: map[string]string{
		"api-us1.tokenkey.dev": `{"code":0,"data":{"accounts":[]}}`,
	}}
	clk := newTestClock()
	agg := NewEdgeAccountsAggregator(store, doer)
	agg.now = clk.now

	_, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)
	require.Equal(t, int64(1), doer.calls.Load())

	clk.advance(edgeAccountsSoftTTL + time.Second) // now stale
	out, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)
	require.Len(t, out.Edges, 1, "stale read still serves the cached aggregate")

	// The background refresh fires asynchronously; wait for the second fan-out.
	require.Eventually(t, func() bool { return doer.calls.Load() == 2 }, time.Second, 5*time.Millisecond,
		"stale read must trigger one background refresh")

	// A subsequent read is fresh again (refreshed cache, clock unchanged) — no new fan-out.
	_, err = agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)
	require.Equal(t, int64(2), doer.calls.Load())
}

func TestEdgeIDFromBaseURL(t *testing.T) {
	require.Equal(t, "us1", edgeIDFromBaseURL("https://api-us1.tokenkey.dev"))
	require.Equal(t, "fra1", edgeIDFromBaseURL("https://api-fra1.tokenkey.dev"))
	// already normalized (no trailing slash) — fallback path for non-matching host
	require.Equal(t, "example.com", edgeIDFromBaseURL("https://example.com"))
}

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
	platformSeen map[string]string // host -> ?platform= query value (last seen)
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
	if f.platformSeen == nil {
		f.platformSeen = map[string]string{}
	}
	f.platformSeen[host] = req.URL.Query().Get("platform")

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

// TestEdgeAccountsAggregator_SurfacesStubGroupAndCooldown proves the prod mirror
// stub's group + variable cooldown snapshot rides into EdgeAccountsResult so the
// overview can filter edges by the PROD stub (not the edge-local accounts). Group
// names are trimmed, deduped, and sorted (ETag stability); the rate-limit cooldown
// is forwarded verbatim. An unreachable edge still carries its stub group (the data
// comes from the prod DB, not the edge).
func TestEdgeAccountsAggregator_SurfacesStubGroupAndCooldown(t *testing.T) {
	rl := mirrorStub(1, "https://api-us1.tokenkey.dev", "key-us1")
	// Duplicate + blank + unsorted group names exercise the normalization.
	rl.Groups = []*Group{{ID: 1, Name: "default"}, {ID: 2, Name: "default"}, {ID: 3, Name: "GPT 专线"}, {ID: 4, Name: "  "}}
	reset := time.Unix(1_700_009_999, 0)
	rl.RateLimitResetAt = &reset

	// An unreachable edge whose stub belongs to a group: the group/status must still
	// surface even though the fan-out to the edge fails.
	dead := mirrorStub(2, "https://api-fra1.tokenkey.dev", "key-fra1")
	dead.Groups = []*Group{{ID: 5, Name: "eu"}}

	store := &edgeAccountsStoreStub{accounts: []Account{rl, dead}}
	doer := &fakeEdgeDoer{
		bodyByHost: map[string]string{"api-us1.tokenkey.dev": `{"code":0,"data":{"accounts":[]}}`},
		errByHost:  map[string]error{"api-fra1.tokenkey.dev": errors.New("connection timeout")},
	}

	agg := NewEdgeAccountsAggregator(store, doer)
	out, err := agg.Aggregate(context.Background(), "anthropic")
	require.NoError(t, err)
	require.Len(t, out.Edges, 2)

	// Sorted by edge_id → fra1 (unreachable) first, us1 second.
	fra1, us1 := out.Edges[0], out.Edges[1]
	require.Equal(t, "fra1", fra1.EdgeID)
	require.Equal(t, "us1", us1.EdgeID)

	// us1: reachable, group names trimmed/deduped/sorted (byte order: 'G' < 'd'),
	// rate-limit cooldown carried, no temp-unsched cooldown.
	require.True(t, us1.OK)
	require.Equal(t, []string{"GPT 专线", "default"}, us1.StubGroups)
	require.NotNil(t, us1.StubRateLimitResetAt)
	require.True(t, us1.StubRateLimitResetAt.Equal(reset))
	require.Nil(t, us1.StubTempUnschedulableUntil)

	// fra1: unreachable, but its stub group still surfaces for filtering.
	require.False(t, fra1.OK)
	require.Equal(t, []string{"eu"}, fra1.StubGroups)
}

// TestStubGroupNames covers the normalization in isolation: nil-safe, blank-skip,
// dedupe, sort, and the non-nil empty slice for an ungrouped stub.
func TestStubGroupNames(t *testing.T) {
	require.Equal(t, []string{}, stubGroupNames(nil))
	require.Equal(t, []string{}, stubGroupNames(&Account{}))
	got := stubGroupNames(&Account{Groups: []*Group{
		{ID: 1, Name: "b"}, nil, {ID: 2, Name: " a "}, {ID: 3, Name: "b"}, {ID: 4, Name: ""},
	}})
	require.Equal(t, []string{"a", "b"}, got)
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

// TestIsEdgeMirrorStub covers the v2-widened predicate: ANY platform's api-key
// account whose base_url matches the edge pattern is a mirror stub (the panel must
// expand openai/grok/kiro/… stubs, not just anthropic).
func TestIsEdgeMirrorStub(t *testing.T) {
	mk := func(platform, typ, baseURL string) *Account {
		return &Account{Platform: platform, Type: typ, Credentials: map[string]any{"base_url": baseURL}}
	}
	cases := []struct {
		name string
		acc  *Account
		want bool
	}{
		{"anthropic apikey + edge base_url", mk(PlatformAnthropic, AccountTypeAPIKey, "https://api-us4.tokenkey.dev"), true},
		{"openai apikey + edge base_url (v2 widened)", mk(PlatformOpenAI, AccountTypeAPIKey, "https://api-us3.tokenkey.dev"), true},
		{"grok apikey + edge base_url (v2 widened)", mk(PlatformGrok, AccountTypeAPIKey, "https://api-us4.tokenkey.dev"), true},
		{"kiro apikey + edge base_url (v2 widened)", mk(PlatformKiro, AccountTypeAPIKey, "https://api-uk2.tokenkey.dev"), true},
		{"oauth type is never a mirror stub", mk(PlatformAnthropic, AccountTypeOAuth, "https://api-us4.tokenkey.dev"), false},
		{"non-edge base_url", mk(PlatformAnthropic, AccountTypeAPIKey, "https://api.anthropic.com"), false},
		{"empty base_url", mk(PlatformAnthropic, AccountTypeAPIKey, ""), false},
		{"nil account", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isEdgeMirrorStub(tc.acc, edgeIDPattern))
		})
	}
}

// platformAwareEdgeStore filters by platform like the real
// accountRepository.ListByPlatform (the shared edgeAccountsStoreStub returns the same
// slice for every platform, which would duplicate across loadEdgeStubCandidates' union
// over edgeStubPlatforms). Used by the by-stub end-to-end test below.
type platformAwareEdgeStore struct{ byPlatform map[string][]Account }

func (s *platformAwareEdgeStore) ListByPlatform(_ context.Context, p string) ([]Account, error) {
	return s.byPlatform[p], nil
}

// TestEdgeAccountsAggregator_ByStubDiscoversGrokAPIKeyRelay covers the converged
// native grok relay stub (platform=grok, type=apikey, edge key in credentials.api_key).
// The full
// by-stub path (AggregateByStub → loadEdgeStubCandidates → ListByPlatform →
// discoverStubTargets → fetchEdgeAccounts) must (a) discover it, keyed by its prod stub
// account id so the inline panel resolves, and (b) authenticate the fan-out with the
// api_key.
func TestEdgeAccountsAggregator_ByStubDiscoversGrokAPIKeyRelay(t *testing.T) {
	grok := Account{
		ID: 65, Name: "grok-us4", Platform: PlatformGrok, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"base_url": "https://api-us4.tokenkey.dev",
			"api_key":  "grok-edge-key",
		},
	}
	store := &platformAwareEdgeStore{byPlatform: map[string][]Account{PlatformGrok: {grok}}}
	doer := &fakeEdgeDoer{bodyByHost: map[string]string{
		"api-us4.tokenkey.dev": `{"data":{"accounts":[],"group":""}}`,
	}}
	agg := NewEdgeAccountsAggregator(store, doer)

	out, err := agg.AggregateByStub(context.Background())
	require.NoError(t, err)

	var got *EdgeAccountsResult
	for i := range out.Edges {
		if out.Edges[i].StubAccountID == 65 {
			got = &out.Edges[i]
		}
	}
	require.NotNil(t, got, "grok-us4 (id 65) must be discovered so the inline panel resolves (not 'edge not discovered')")
	require.True(t, got.OK, "the edge fan-out must succeed")
	require.Equal(t, "us4", got.EdgeID)
	require.Equal(t, PlatformGrok, got.StubPlatform)
	require.Equal(t, "grok-edge-key", doer.keysSeen["api-us4.tokenkey.dev"],
		"the fan-out must authenticate with the grok stub's api_key")
}

// TestEdgeAccountsAggregator_ByStubDiscoversLegacyGrokNewAPIBridge keeps the old
// newapi bridge shape observable until prod migration is complete. Two earlier gaps
// combined to render the phantom panel even though the stub relayed actively:
//
//  1. edgeStubPlatforms excluded newapi, so loadEdgeStubCandidates never loaded it →
//     discoverStubTargets never saw it → no by-stub result for id 65 (while the
//     platform-agnostic MirrorStubEdgeID still tagged the prod row, so the panel showed).
//  2. Even loaded, its own platform "newapi" is a transport label — the edge has no
//     newapi pool (it serves grok under platform=grok), so ?platform=newapi → 0 accounts.
//
// The fan-out must now (a) discover it (newapi in edgeStubPlatforms), (b) query the edge
// with the "all" sentinel (newapi → all in edgeStubPoolPlatform) so group_scope=caller
// resolves the edge's grok pool, (c) authenticate with the api_key, and (d) surface the
// edge's grok account + the edge-side group name ("grok") for the panel footnote.
func TestEdgeAccountsAggregator_ByStubDiscoversLegacyGrokNewAPIBridge(t *testing.T) {
	grok := Account{
		ID: 65, Name: "grok-us4", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: 1, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"base_url": "https://api-us4.tokenkey.dev",
			"api_key":  "grok-bridge-key", // edge TokenKey key; NO access_token, NO mirror_platform
		},
	}
	store := &platformAwareEdgeStore{byPlatform: map[string][]Account{PlatformNewAPI: {grok}}}
	// The edge serves the grok pool under platform=grok and reports the caller key's
	// edge-side group ("grok") — exactly what the live us4 edge returns for this key.
	doer := &fakeEdgeDoer{bodyByHost: map[string]string{
		"api-us4.tokenkey.dev": `{"data":{"accounts":[{"id":6,"name":"grok-or1-ls-b","platform":"grok","type":"oauth","status":"active"}],"group":"grok"}}`,
	}}
	agg := NewEdgeAccountsAggregator(store, doer)

	out, err := agg.AggregateByStub(context.Background())
	require.NoError(t, err)

	var got *EdgeAccountsResult
	for i := range out.Edges {
		if out.Edges[i].StubAccountID == 65 {
			got = &out.Edges[i]
		}
	}
	require.NotNil(t, got, "legacy newapi grok bridge grok-us4 (id 65) must be discovered so the inline panel resolves (not '该 edge 暂未被发现')")
	require.True(t, got.OK, "the edge fan-out must succeed")
	require.Equal(t, "us4", got.EdgeID)
	require.Equal(t, edgeStubAllPoolsPlatform, got.StubPlatform,
		"a newapi transport stub fans out as the 'all' sentinel, not its non-pool 'newapi' platform")
	require.Equal(t, "all", doer.platformSeen["api-us4.tokenkey.dev"],
		"the fan-out must query ?platform=all so group_scope=caller resolves the edge's grok pool, not ?platform=newapi (0 accounts)")
	require.Equal(t, "grok-bridge-key", doer.keysSeen["api-us4.tokenkey.dev"],
		"the fan-out must authenticate with the bridge stub's api_key")
	require.Equal(t, "grok", got.EdgeGroup, "the panel footnote names the edge-side group from the edge response")
	require.Len(t, got.Accounts, 1, "the edge's grok account must surface in the panel")
}

// TestDiscoverStubTargets covers the per-stub discovery: NO dedup by edge host
// (cc-us4 / openai-us4 / grok-us4 share one edge but are three distinct targets),
// all-platform, carrying stub id + platform + the caller-group-scope flag; disabled
// and credential-incomplete stubs are skipped.
func TestDiscoverStubTargets(t *testing.T) {
	mk := func(id int64, platform, baseURL, apiKey string) Account {
		return Account{
			ID: id, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{"base_url": baseURL, "api_key": apiKey},
		}
	}
	disabled := mk(5, PlatformAnthropic, "https://api-uk9.tokenkey.dev", "k5")
	disabled.Status = StatusDisabled
	noKey := mk(6, PlatformOpenAI, "https://api-uk8.tokenkey.dev", "")

	// kiro mirror stub: TRANSPORT platform is anthropic-apikey, but
	// credentials.mirror_platform=kiro declares it represents the edge's KIRO pool.
	// It SHARES the edge host (api-us4) with the anthropic stub k1 — the exact
	// cc-us6 / kiro-us6 topology — so the fan-out must query platform=kiro for it,
	// not the anthropic pool they co-locate on.
	kiroMirror := mk(8, PlatformAnthropic, "https://api-us4.tokenkey.dev", "k8")
	kiroMirror.Credentials["mirror_platform"] = "kiro"

	// grok mirror stub: the long-term shape is platform=grok,type=apikey with
	// credentials.api_key. It MUST be discovered and use api_key as the edge key.
	grokEdge := mk(9, PlatformGrok, "https://api-us4.tokenkey.dev", "grok-edge-key")

	// Legacy prod grok edge stub: a New API ct=1 OpenAI-compat bridge (platform=newapi,
	// api_key, base_url=edge host, NO mirror_platform). Keep it discoverable during
	// migration, fanning out as the "all" sentinel so group_scope=caller resolves the
	// edge's real grok pool (the edge has no newapi pool).
	grokNewAPIBridge := mk(65, PlatformNewAPI, "https://api-us4.tokenkey.dev", "grok-bridge-key")
	grokNewAPIBridge.ChannelType = 1

	accounts := []Account{
		mk(1, PlatformAnthropic, "https://api-us4.tokenkey.dev", "k1"),
		mk(2, PlatformOpenAI, "https://api-us4.tokenkey.dev", "k2"), // SAME edge host — NOT deduped
		mk(3, PlatformGrok, "https://api-us4.tokenkey.dev", "k3"),
		kiroMirror,       // SAME edge host as k1 — kiro pool via mirror_platform, NOT anthropic
		grokEdge,         // SAME edge host — grok edge key in api_key
		grokNewAPIBridge, // SAME edge host — newapi transport → "all" pool, api_key auth
		disabled,         // skipped (disabled)
		noKey,            // skipped (no api_key, no access_token fallback)
		// non-edge base_url → not a stub
		{ID: 7, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive,
			Credentials: map[string]any{"base_url": "https://generativelanguage.googleapis.com"}},
	}

	targets := discoverStubTargets(accounts, edgeIDPattern)
	require.Len(t, targets, 6) // six distinct stubs on one edge, NOT deduped

	byID := map[int64]edgeTarget{}
	for _, tg := range targets {
		byID[tg.stubAccountID] = tg
		require.True(t, tg.groupScopeCaller, "per-stub targets must request caller-group scope")
		require.Equal(t, "us4", tg.edgeID)
	}
	require.Equal(t, PlatformAnthropic, byID[1].platform)
	require.Equal(t, PlatformOpenAI, byID[2].platform)
	require.Equal(t, PlatformGrok, byID[3].platform)
	// The kiro mirror stub fans out as KIRO (from mirror_platform), NOT anthropic
	// (its transport platform) — the fix for the cc-us6/kiro-us6 mixing bug.
	require.Equal(t, PlatformKiro, byID[8].platform)
	// The converged grok edge stub is discovered and authenticates with api_key.
	require.Contains(t, byID, int64(9), "grok edge stub (api_key relay) must be discovered")
	require.Equal(t, PlatformGrok, byID[9].platform)
	require.Equal(t, "grok-edge-key", byID[9].apiKey)
	// The REAL newapi grok bridge stub is discovered and fans out as the "all" sentinel
	// (transport-only platform → let group_scope=caller pick the edge's grok pool),
	// authenticating with its api_key — the fix for grok-us4's phantom panel.
	require.Contains(t, byID, int64(65), "newapi grok bridge stub (id 65) must be discovered")
	require.Equal(t, edgeStubAllPoolsPlatform, byID[65].platform)
	require.Equal(t, "grok-bridge-key", byID[65].apiKey)
}

// TestEdgeStubPoolPlatform covers the transport-vs-pool resolution: a stub's
// credentials.mirror_platform (when set) is the authoritative edge pool, otherwise
// the stub's own platform. This is what keeps the kiro mirror stub (anthropic
// transport) from querying the anthropic pool it shares an edge host with.
func TestEdgeStubPoolPlatform(t *testing.T) {
	mk := func(platform string, cred map[string]any) *Account {
		return &Account{Platform: platform, Type: AccountTypeAPIKey, Credentials: cred}
	}
	cases := []struct {
		name string
		acc  *Account
		want string
	}{
		{"anthropic stub, no mirror_platform → anthropic", mk(PlatformAnthropic, map[string]any{}), PlatformAnthropic},
		{"kiro mirror over anthropic transport → kiro", mk(PlatformAnthropic, map[string]any{"mirror_platform": "kiro"}), PlatformKiro},
		{"empty mirror_platform falls back to own platform (NOT anthropic)", mk(PlatformOpenAI, map[string]any{"mirror_platform": "  "}), PlatformOpenAI},
		{"mirror_platform is trimmed + lowercased", mk(PlatformAnthropic, map[string]any{"mirror_platform": " Kiro "}), PlatformKiro},
		{"native openai stub, no mirror_platform → openai", mk(PlatformOpenAI, nil), PlatformOpenAI},
		// newapi is a TRANSPORT label (the prod grok ct=1 bridge), never an edge pool —
		// the edge has no newapi pool, it serves grok under platform=grok. With no
		// mirror_platform the resolver must NOT query ?platform=newapi (edge → 0 accounts);
		// it returns the "all" sentinel so group_scope=caller narrows to the key's real
		// edge group. This is the fix for grok-us4's "该 edge 暂未被发现" phantom panel.
		{"newapi bridge stub, no mirror_platform → all (transport, not an edge pool)", mk(PlatformNewAPI, map[string]any{"api_key": "k"}), edgeStubAllPoolsPlatform},
		{"newapi bridge stub with mirror_platform=grok → grok (explicit pool wins)", mk(PlatformNewAPI, map[string]any{"api_key": "k", "mirror_platform": "grok"}), PlatformGrok},
		{"nil account → empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, edgeStubPoolPlatform(tc.acc))
		})
	}
}

// TestEdgeStubAPIKey covers edge-key resolution: converged relay stubs authenticate
// with credentials.api_key. A grok access_token fallback remains only for old stubs.
func TestEdgeStubAPIKey(t *testing.T) {
	mk := func(platform string, cred map[string]any) *Account {
		return &Account{Platform: platform, Type: AccountTypeAPIKey, Credentials: cred}
	}
	cases := []struct {
		name string
		acc  *Account
		want string
	}{
		{"anthropic stub → api_key", mk(PlatformAnthropic, map[string]any{"api_key": "ak"}), "ak"},
		{"openai stub → api_key", mk(PlatformOpenAI, map[string]any{"api_key": "ok"}), "ok"},
		{"kiro mirror (anthropic transport) → api_key", mk(PlatformAnthropic, map[string]any{"api_key": "kk", "mirror_platform": "kiro"}), "kk"},
		{"grok stub → api_key", mk(PlatformGrok, map[string]any{"api_key": "gk"}), "gk"},
		{"grok stub prefers api_key over legacy access_token", mk(PlatformGrok, map[string]any{"access_token": "legacy", "api_key": "gk"}), "gk"},
		{"grok legacy access_token fallback", mk(PlatformGrok, map[string]any{"access_token": "gt"}), "gt"},
		{"api_key/access_token trimmed", mk(PlatformGrok, map[string]any{"api_key": "  gk  "}), "gk"},
		{"missing edge key → empty (skipped by discovery)", mk(PlatformGrok, map[string]any{}), ""},
		{"nil account → empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, edgeStubAPIKey(tc.acc))
		})
	}
}

//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type edgeAccountsStoreStub struct {
	accounts []Account
	err      error
}

func (s *edgeAccountsStoreStub) ListByPlatform(_ context.Context, _ string) ([]Account, error) {
	return s.accounts, s.err
}

// fakeEdgeDoer routes by request host, records the x-api-key seen per host, and
// returns a canned response or error.
type fakeEdgeDoer struct {
	mu           sync.Mutex
	bodyByHost   map[string]string // host -> JSON body for a 200
	errByHost    map[string]error  // host -> transport error
	statusByHost map[string]int    // host -> non-200 status
	keysSeen     map[string]string // host -> x-api-key
}

func (f *fakeEdgeDoer) Do(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	f.mu.Lock()
	if f.keysSeen == nil {
		f.keysSeen = map[string]string{}
	}
	f.keysSeen[host] = req.Header.Get("x-api-key")
	f.mu.Unlock()

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

	// Both edges present (sorted): us1 (schedulable) and us4 (paused).
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

func TestEdgeIDFromBaseURL(t *testing.T) {
	require.Equal(t, "us1", edgeIDFromBaseURL("https://api-us1.tokenkey.dev"))
	require.Equal(t, "fra1", edgeIDFromBaseURL("https://api-fra1.tokenkey.dev"))
	// already normalized (no trailing slash) — fallback path for non-matching host
	require.Equal(t, "example.com", edgeIDFromBaseURL("https://example.com"))
}

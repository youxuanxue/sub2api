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
	return Account{
		ID:       id,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
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

	// fra1: transport error → ok:false, isolated (aggregate still succeeds).
	require.False(t, out.Edges[0].OK)
	require.Contains(t, out.Edges[0].Error, "request failed")
	require.Empty(t, out.Edges[0].Accounts)

	// us1: ok with one account; the FIRST stub's api_key is used (dedup kept first).
	require.True(t, out.Edges[1].OK)
	require.Len(t, out.Edges[1].Accounts, 1)
	require.Equal(t, int64(11), out.Edges[1].Accounts[0].ID)

	require.Equal(t, "key-us1", doer.keysSeen["api-us1.tokenkey.dev"])
	require.Equal(t, "key-fra1", doer.keysSeen["api-fra1.tokenkey.dev"])
	// sg1 (oauth) and example.com (non-edge) must never have been called.
	require.NotContains(t, doer.keysSeen, "api-sg1.tokenkey.dev")
	require.NotContains(t, doer.keysSeen, "example.com")
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

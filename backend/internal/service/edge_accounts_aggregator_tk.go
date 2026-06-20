package service

// EdgeAccountsAggregator is the prod-side consumer of the edge read-only account
// inventory endpoint (handler.EdgeAccountsHandler / GET /api/v1/edge/accounts).
//
// It powers the prod admin "Edge Accounts" overview: discover every edge via the
// local anthropic mirror stubs (the same {base_url, api_key} pairs surface-C's
// concurrency mirror already uses — zero new config, zero new secret), then
// fan out concurrently and read each edge's account list.
//
// Boundaries (mirroring anthropic_config_reconciler.go's surface-C doctrine):
//   - READ ONLY. No writes anywhere. No scheduling side effects.
//   - Per-edge failure is ISOLATED: a timeout / non-2xx / decode error on one
//     edge yields {ok:false, error} for that edge and never fails the aggregate.
//   - Credentials never traverse this path: the edge endpoint already returns a
//     credential-free DTO, and prod only ever decodes that sanitized shape.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/baseline"
)

// ErrEdgeNotFound is returned when an edge_id does not resolve to a known mirror
// stub (so the prod admin handler can map it to a 404 rather than a 500).
var ErrEdgeNotFound = errors.New("edge not found")

const (
	// edgeAccountsHTTPTO bounds a single edge read. Matches surface-C's 8s budget.
	edgeAccountsHTTPTO = 8 * time.Second
	// edgeAccountsFanoutCap bounds concurrent edge reads — cheap insurance if the
	// mirror-stub count ever grows large; today the fleet is well under this.
	edgeAccountsFanoutCap = 16
	// edgeAccountsSoftTTL is how long a cached aggregate is served as-is before a
	// background refresh is kicked off. The overview is a read-only ops dashboard,
	// so a few seconds of staleness is fine; in exchange, a slow/dead edge (up to
	// edgeAccountsHTTPTO per fan-out) only ever costs the background goroutine, never
	// the operator's request. After the first (cold) load, every load/refresh —
	// manual button, periodic auto-refresh, a second admin — returns instantly.
	edgeAccountsSoftTTL = 15 * time.Second
)

// edgeIDPattern extracts the edge id from an internal edge base_url:
// https://api-<edge_id>.tokenkey.dev → <edge_id> (e.g. api-us1 → us1).
var edgeIDPattern = regexp.MustCompile(`^https?://api-([a-z0-9]+)\.tokenkey\.dev/?$`)

// edgeAccountsStore is the narrow account dependency: list anthropic accounts so
// the mirror stubs can be discovered. *accountRepository satisfies it via the
// existing ListByPlatform — no AccountRepository interface change (rule 6).
type edgeAccountsStore interface {
	ListByPlatform(ctx context.Context, platform string) ([]Account, error)
}

// EdgeAccountsResult is one edge's slice of the aggregate. OK distinguishes a
// reachable edge (even with zero accounts) from an unreachable one (Error set).
//
// Accounts is carried as opaque json.RawMessage: the per-account shape (the
// sanitized, credential-free edgeAccountDTO with its live capacity/today gauges)
// is owned solely by the edge handler and the frontend TS type. Prod just relays
// it verbatim, so adding an account field never requires touching this file.
//
// StubSchedulable mirrors the prod-side mirror stub's own scheduling toggle (the
// account whose api-key prod uses to reach this edge). It is the operator's
// "route prod traffic to this edge / don't" switch: turning scheduling off on the
// stub (关调度) takes the edge out of prod rotation while leaving the edge itself
// reachable. The overview lists the edge's own accounts regardless, so without
// surfacing this the operator can't tell from the overview that prod has stopped
// routing here. It is false only when the stub is reachable-but-paused; a fully
// disabled stub is dropped from the aggregate entirely (see discoverEdgeTargets).
type EdgeAccountsResult struct {
	EdgeID          string `json:"edge_id"`
	BaseURL         string `json:"base_url"`
	OK              bool   `json:"ok"`
	Error           string `json:"error,omitempty"`
	StubSchedulable bool   `json:"stub_schedulable"`

	// Prod-stub status/group snapshot (TK). The read-only overview filters edges by
	// the PROD mirror stub's own group + status — NOT the edge-local accounts' — so
	// the operator filters the fleet the way prod organizes it (the stub is prod's
	// handle for an edge) and judges health end-to-end (prod's relay to the edge AND
	// the edge account). The frontend:
	//   - 分组 dropdown + filter key on StubGroups;
	//   - 状态 filter combines the stub's schedulable/cooldown state with each edge
	//     account's own status (正常 = stub正常 AND account正常; any other bucket =
	//     stub OR account).
	// Only the GENUINELY-VARIABLE stub fields are surfaced: the stub's status column
	// is provably constant "active" here (ListByPlatform pins status='active' and
	// discovery additionally skips StatusDisabled), so it is not plumbed — the
	// frontend hard-codes 'active' for the stub when reusing the shared status
	// predicate. These ride alongside StubSchedulable (the existing "调度已关闭" badge).
	StubRateLimitResetAt       *time.Time `json:"stub_rate_limit_reset_at,omitempty"`
	StubTempUnschedulableUntil *time.Time `json:"stub_temp_unschedulable_until,omitempty"`
	StubGroups                 []string   `json:"stub_groups"`

	Accounts []json.RawMessage `json:"accounts"`
}

// EdgeAccountsAggregate is the full cross-edge payload returned to the admin UI.
type EdgeAccountsAggregate struct {
	Platform string               `json:"platform"`
	Edges    []EdgeAccountsResult `json:"edges"`
	TS       int64                `json:"ts"`
}

// cachedAggregate is one platform's last successfully fanned-out aggregate plus
// when it was produced, so Aggregate can serve it within edgeAccountsSoftTTL and
// fall back to it (stale) while a background refresh is in flight.
type cachedAggregate struct {
	agg       *EdgeAccountsAggregate
	fetchedAt time.Time
}

// EdgeAccountsAggregator discovers edges and fans out the read.
//
// A per-platform stale-while-revalidate cache fronts the fan-out: see Aggregate.
// The fan-out itself (discover + concurrent edge reads + sort) is in fanout.
type EdgeAccountsAggregator struct {
	accounts edgeAccountsStore
	http     httpDoer

	// now is injectable so tests can drive the soft-TTL clock; defaults to time.Now.
	now func() time.Time

	mu         sync.Mutex
	cache      map[string]cachedAggregate // platform -> last good aggregate
	refreshing map[string]bool            // platform -> a background refresh is in flight
}

// NewEdgeAccountsAggregator constructs the aggregator. A nil accounts store makes
// Aggregate return an empty (non-error) result, keeping wire wiring safe.
func NewEdgeAccountsAggregator(accounts edgeAccountsStore, client httpDoer) *EdgeAccountsAggregator {
	return &EdgeAccountsAggregator{
		accounts:   accounts,
		http:       client,
		now:        time.Now,
		cache:      make(map[string]cachedAggregate),
		refreshing: make(map[string]bool),
	}
}

// edgeTarget is a deduped {base_url, api_key, edge_id} discovered from a stub,
// plus the stub's own scheduling toggle so the overview can show whether prod is
// still routing to this edge (see EdgeAccountsResult.StubSchedulable).
type edgeTarget struct {
	edgeID      string
	baseURL     string
	apiKey      string
	schedulable bool

	// Prod-stub group + variable cooldown snapshot (TK), forwarded into
	// EdgeAccountsResult so the overview can filter edges by the prod stub's group +
	// combined status. The stub's status column is always "active" (active-only
	// discovery), so it is not carried — see EdgeAccountsResult's comment.
	rateLimitResetAt       *time.Time
	tempUnschedulableUntil *time.Time
	groups                 []string
}

// Aggregate returns the cross-edge account inventory for the given platform,
// fronted by a per-platform stale-while-revalidate cache so a slow/dead edge never
// drags the operator's request:
//
//   - fresh hit (age < edgeAccountsSoftTTL): return the cached aggregate at once;
//   - stale hit: return the (stale) cached aggregate at once AND launch a
//     single-flight background refresh so the next caller gets fresher data;
//   - cold (no cache yet): synchronously fan out — the only slow path, paid once.
//
// The actual discovery + concurrent edge reads live in fanout.
func (a *EdgeAccountsAggregator) Aggregate(ctx context.Context, platform string) (*EdgeAccountsAggregate, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		platform = PlatformAnthropic
	}
	if a == nil || a.accounts == nil {
		return &EdgeAccountsAggregate{Platform: platform, Edges: []EdgeAccountsResult{}, TS: a.nowUnix()}, nil
	}

	a.mu.Lock()
	entry, ok := a.cache[platform]
	if ok {
		stale := a.now().Sub(entry.fetchedAt) >= edgeAccountsSoftTTL
		if stale && !a.refreshing[platform] {
			a.refreshing[platform] = true
			go a.refreshAsync(platform)
		}
		a.mu.Unlock()
		return entry.agg, nil
	}
	a.mu.Unlock()

	// Cold cache: fan out synchronously (first load only).
	agg, err := a.fanout(ctx, platform)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.cache[platform] = cachedAggregate{agg: agg, fetchedAt: a.now()}
	a.mu.Unlock()
	return agg, nil
}

// refreshAsync re-fans-out a platform in the background and replaces its cache
// entry on success. It uses context.Background() (NOT a request context, which is
// cancelled the moment the serving request returns) with its own timeout; on
// failure it keeps the stale entry so the overview never regresses to empty.
func (a *EdgeAccountsAggregator) refreshAsync(platform string) {
	defer func() {
		a.mu.Lock()
		a.refreshing[platform] = false
		a.mu.Unlock()
	}()
	// Bound the whole fan-out: edge reads are individually capped at
	// edgeAccountsHTTPTO, run concurrently, so a small margin over that covers
	// discovery + scheduling.
	ctx, cancel := context.WithTimeout(context.Background(), edgeAccountsHTTPTO+2*time.Second)
	defer cancel()
	agg, err := a.fanout(ctx, platform)
	if err != nil {
		slog.Warn("edge accounts aggregator: background refresh failed; keeping stale cache",
			"platform", platform, "error", err)
		return
	}
	a.mu.Lock()
	a.cache[platform] = cachedAggregate{agg: agg, fetchedAt: a.now()}
	a.mu.Unlock()
}

// nowUnix returns the current unix timestamp via the injectable clock.
func (a *EdgeAccountsAggregator) nowUnix() int64 {
	if a == nil || a.now == nil {
		return time.Now().Unix()
	}
	return a.now().Unix()
}

// fanout discovers mirror-stub edges and concurrently reads each edge's account
// inventory for the given platform. The discovery scan itself is always against
// anthropic accounts (mirror stubs are anthropic api-key accounts); the platform
// argument is forwarded to each edge's /accounts query. Caller owns caching.
func (a *EdgeAccountsAggregator) fanout(ctx context.Context, platform string) (*EdgeAccountsAggregate, error) {
	out := &EdgeAccountsAggregate{Platform: platform, Edges: []EdgeAccountsResult{}, TS: a.nowUnix()}
	if a == nil || a.accounts == nil {
		return out, nil
	}

	stubs, err := a.accounts.ListByPlatform(ctx, PlatformAnthropic)
	if err != nil {
		return nil, err
	}
	_, re, err := baseline.LoadStubPoolBaseline()
	if err != nil {
		return nil, err
	}

	targets := discoverEdgeTargets(stubs, re)
	if len(targets) == 0 {
		return out, nil
	}

	results := make([]EdgeAccountsResult, len(targets))
	sem := make(chan struct{}, edgeAccountsFanoutCap)
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(idx int, t edgeTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = a.fetchEdgeAccounts(ctx, t, platform)
		}(i, targets[i])
	}
	wg.Wait()

	// Scheduling-off edges (the prod stub is active-but-关调度, so prod has stopped
	// routing there) sink to the bottom of the overview: the operator's live,
	// in-rotation edges surface first, paused ones cluster at the end next to their
	// amber "调度已关闭" badge. Within each band the order stays stable by edge_id so
	// the list is deterministic across refreshes.
	sort.Slice(results, func(i, j int) bool {
		if results[i].StubSchedulable != results[j].StubSchedulable {
			return results[i].StubSchedulable // schedulable (true) before paused (false)
		}
		return results[i].EdgeID < results[j].EdgeID
	})
	out.Edges = results
	return out, nil
}

// discoverEdgeTargets filters the anthropic accounts down to mirror stubs and
// dedups by normalized base_url (multiple stubs may point at one edge — keep the
// first, log the dupe). Stubs missing base_url/api_key are skipped.
func discoverEdgeTargets(accounts []Account, re *regexp.Regexp) []edgeTarget {
	seen := make(map[string]struct{})
	targets := make([]edgeTarget, 0, len(accounts))
	for i := range accounts {
		acct := &accounts[i]
		if !isAnthropicMirrorStub(acct, re) {
			continue
		}
		// Skip operator-disabled stubs: a disabled mirror stub means the edge was
		// deliberately taken out of rotation (e.g. a decommissioned region whose
		// DNS no longer resolves). Fanning out to it every refresh only ever
		// surfaces a permanent failure card and risks an 8s timeout if its host
		// blackholes — neither is useful on a read-only overview. 'error' (a
		// transient runtime state) is intentionally NOT skipped: the edge may
		// still be reachable, and another stub for the same base_url may cover it.
		if acct.Status == StatusDisabled {
			continue
		}
		baseURL := normalizeEdgeBaseURL(acct.GetCredential("base_url"))
		apiKey := strings.TrimSpace(acct.GetCredential("api_key"))
		if baseURL == "" || apiKey == "" {
			continue
		}
		if _, dup := seen[baseURL]; dup {
			slog.Debug("edge accounts aggregator: duplicate mirror stub base_url; keeping first",
				"account_id", acct.ID, "base_url", baseURL)
			continue
		}
		seen[baseURL] = struct{}{}
		// Keep-first per base_url: the edge's schedulable/cooldown/group snapshot is
		// keyed to this single canonical stub. The documented topology is one
		// cc-<edge> mirror stub per edge, so two active same-base_url stubs in
		// different prod groups is a misconfiguration — not a multi-group edge — and
		// only the first stub's groups drive the 分组 filter (matching the pre-existing
		// keep-first scheduling semantics).
		targets = append(targets, edgeTarget{
			edgeID:                 edgeIDFromBaseURL(baseURL),
			baseURL:                baseURL,
			apiKey:                 apiKey,
			schedulable:            acct.Schedulable,
			rateLimitResetAt:       acct.RateLimitResetAt,
			tempUnschedulableUntil: acct.TempUnschedulableUntil,
			groups:                 stubGroupNames(acct),
		})
	}
	return targets
}

// stubGroupNames extracts a prod mirror stub's group names (trimmed, blank-skipped,
// deduped, sorted) for the overview's 分组 filter. The Edge Accounts page groups
// edges by the PROD stub's group — prod's handle for the edge — not by the
// edge-local accounts' own groups, so the operator filters the fleet the way prod
// organizes it. Always returns a non-nil slice so the JSON is `[]` (not null) and
// the ETag stays stable for an ungrouped stub. Sorting keeps the order (hence the
// ETag) deterministic across fan-outs regardless of DB row order.
func stubGroupNames(a *Account) []string {
	if a == nil || len(a.Groups) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(a.Groups))
	names := make([]string, 0, len(a.Groups))
	for _, g := range a.Groups {
		if g == nil {
			continue
		}
		n := strings.TrimSpace(g.Name)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// isAnthropicMirrorStub mirrors AnthropicConfigReconciler.isMirrorStub: an
// anthropic api-key account whose base_url matches the internal-edge pattern.
// Duplicated here (rather than coupling to the reconciler's unexported method)
// because the predicate is tiny and stable; both read the same baseline regex.
func isAnthropicMirrorStub(a *Account, re *regexp.Regexp) bool {
	if a == nil || re == nil {
		return false
	}
	if a.Platform != PlatformAnthropic || a.Type != AccountTypeAPIKey {
		return false
	}
	baseURL := strings.TrimSpace(a.GetCredential("base_url"))
	return baseURL != "" && re.MatchString(baseURL)
}

// normalizeEdgeBaseURL trims trailing slash and lowercases for dedup/derivation.
func normalizeEdgeBaseURL(raw string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(raw)), "/")
}

// edgeIDFromBaseURL derives a short edge id (api-us1 → us1); falls back to the
// host-ish remainder when the pattern doesn't match.
func edgeIDFromBaseURL(baseURL string) string {
	if m := edgeIDPattern.FindStringSubmatch(baseURL); len(m) == 2 {
		return m[1]
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	if host, _, ok := strings.Cut(trimmed, "/"); ok {
		return host
	}
	return trimmed
}

// MirrorStubEdgeID returns the edge id a prod anthropic mirror stub points at
// (credentials.base_url https://api-us1.tokenkey.dev → "us1"), or "" if acc is
// not a mirror stub.
//
// It is the exported, single-account form the prod admin accounts LIST uses to
// tag each row with its edge (so the frontend knows which rows are edge mirrors
// and which edge to expand). It reuses the SAME predicate + derivation the
// cross-edge aggregator already uses (isAnthropicMirrorStub + edgeIDFromBaseURL
// over the package edgeIDPattern), so the accounts list and the edge overview
// agree on which rows are edge mirrors — no second regex, no baseline reload.
func MirrorStubEdgeID(acc *Account) string {
	if !isAnthropicMirrorStub(acc, edgeIDPattern) {
		return ""
	}
	return edgeIDFromBaseURL(strings.TrimSpace(acc.GetCredential("base_url")))
}

// fetchEdgeAccounts GETs {base_url}/api/v1/edge/accounts?platform=... with
// x-api-key auth and an 8s timeout. Any failure → {ok:false, error}, never a
// panic and never a failed aggregate.
func (a *EdgeAccountsAggregator) fetchEdgeAccounts(ctx context.Context, t edgeTarget, platform string) EdgeAccountsResult {
	// Stub group/cooldown ride from the target (prod DB) regardless of edge
	// reachability — an unreachable edge still carries its prod stub's group + state
	// so the overview can filter it. stubGroupNames guarantees a non-nil slice.
	stubGroups := t.groups
	if stubGroups == nil {
		stubGroups = []string{}
	}
	res := EdgeAccountsResult{
		EdgeID:                     t.edgeID,
		BaseURL:                    t.baseURL,
		StubSchedulable:            t.schedulable,
		StubRateLimitResetAt:       t.rateLimitResetAt,
		StubTempUnschedulableUntil: t.tempUnschedulableUntil,
		StubGroups:                 stubGroups,
		Accounts:                   []json.RawMessage{},
	}
	if a.http == nil {
		res.Error = "no http client"
		return res
	}
	endpoint := t.baseURL + "/api/v1/edge/accounts?platform=" + platform
	reqCtx, cancel := context.WithTimeout(ctx, edgeAccountsHTTPTO)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		res.Error = "build request failed"
		return res
	}
	req.Header.Set("x-api-key", t.apiKey)

	resp, err := a.http.Do(req)
	if err != nil {
		res.Error = "request failed: " + err.Error()
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		res.Error = "edge returned http " + strconv.Itoa(resp.StatusCode)
		return res
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap — account lists are small
	if err != nil {
		res.Error = "read body failed"
		return res
	}
	var env struct {
		Data struct {
			Accounts []json.RawMessage `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		res.Error = "decode body failed"
		return res
	}
	res.OK = true
	if env.Data.Accounts != nil {
		res.Accounts = env.Data.Accounts
	}
	return res
}

// EdgeAdminSession is the minted handoff result for one edge: the renewable admin
// session (access + refresh) plus the edge's base_url so the caller can build the
// handoff URL.
type EdgeAdminSession struct {
	EdgeID       string `json:"edge_id"`
	BaseURL      string `json:"base_url"`
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// resolveTarget discovers the mirror-stub edges and returns the one whose derived
// edge_id matches. ErrEdgeNotFound when no stub matches.
func (a *EdgeAccountsAggregator) resolveTarget(ctx context.Context, edgeID string) (edgeTarget, error) {
	edgeID = strings.ToLower(strings.TrimSpace(edgeID))
	if a == nil || a.accounts == nil || edgeID == "" {
		return edgeTarget{}, ErrEdgeNotFound
	}
	stubs, err := a.accounts.ListByPlatform(ctx, PlatformAnthropic)
	if err != nil {
		return edgeTarget{}, err
	}
	_, re, err := baseline.LoadStubPoolBaseline()
	if err != nil {
		return edgeTarget{}, err
	}
	for _, t := range discoverEdgeTargets(stubs, re) {
		if t.edgeID == edgeID {
			return t, nil
		}
	}
	return edgeTarget{}, ErrEdgeNotFound
}

// MintAdminSession resolves the edge by id and POSTs to its /api/v1/edge/admin-session
// with the mirror-stub x-api-key, returning the short-lived admin JWT + base_url.
// This is the write-direction sibling of fetchEdgeAccounts: same discovery, same
// x-api-key auth, same per-call timeout and failure isolation.
func (a *EdgeAccountsAggregator) MintAdminSession(ctx context.Context, edgeID string) (*EdgeAdminSession, error) {
	t, err := a.resolveTarget(ctx, edgeID)
	if err != nil {
		return nil, err
	}
	if a.http == nil {
		return nil, errors.New("no http client")
	}
	endpoint := t.baseURL + "/api/v1/edge/admin-session"
	reqCtx, cancel := context.WithTimeout(ctx, edgeAccountsHTTPTO)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, nil)
	if err != nil {
		return nil, errors.New("build request failed")
	}
	req.Header.Set("x-api-key", t.apiKey)

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, errors.New("request failed: " + err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("edge returned http " + strconv.Itoa(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, errors.New("read body failed")
	}
	var env struct {
		Data struct {
			Token        string `json:"token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, errors.New("decode body failed")
	}
	if env.Data.Token == "" {
		return nil, errors.New("edge returned empty token")
	}
	return &EdgeAdminSession{
		EdgeID:       t.edgeID,
		BaseURL:      t.baseURL,
		Token:        env.Data.Token,
		RefreshToken: env.Data.RefreshToken,
		ExpiresIn:    env.Data.ExpiresIn,
	}, nil
}

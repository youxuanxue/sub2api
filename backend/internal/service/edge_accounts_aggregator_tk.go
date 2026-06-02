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

const (
	// edgeAccountsHTTPTO bounds a single edge read. Matches surface-C's 8s budget.
	edgeAccountsHTTPTO = 8 * time.Second
	// edgeAccountsFanoutCap bounds concurrent edge reads — cheap insurance if the
	// mirror-stub count ever grows large; today the fleet is well under this.
	edgeAccountsFanoutCap = 16
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
type EdgeAccountsResult struct {
	EdgeID   string            `json:"edge_id"`
	BaseURL  string            `json:"base_url"`
	OK       bool              `json:"ok"`
	Error    string            `json:"error,omitempty"`
	Accounts []json.RawMessage `json:"accounts"`
}

// EdgeAccountsAggregate is the full cross-edge payload returned to the admin UI.
type EdgeAccountsAggregate struct {
	Platform string               `json:"platform"`
	Edges    []EdgeAccountsResult `json:"edges"`
	TS       int64                `json:"ts"`
}

// EdgeAccountsAggregator discovers edges and fans out the read.
type EdgeAccountsAggregator struct {
	accounts edgeAccountsStore
	http     httpDoer
}

// NewEdgeAccountsAggregator constructs the aggregator. A nil accounts store makes
// Aggregate return an empty (non-error) result, keeping wire wiring safe.
func NewEdgeAccountsAggregator(accounts edgeAccountsStore, client httpDoer) *EdgeAccountsAggregator {
	return &EdgeAccountsAggregator{accounts: accounts, http: client}
}

// edgeTarget is a deduped {base_url, api_key, edge_id} discovered from a stub.
type edgeTarget struct {
	edgeID  string
	baseURL string
	apiKey  string
}

// Aggregate discovers mirror-stub edges and concurrently reads each edge's
// account inventory for the given platform. The discovery scan itself is always
// against anthropic accounts (mirror stubs are anthropic api-key accounts); the
// platform argument is forwarded to each edge's /accounts query.
func (a *EdgeAccountsAggregator) Aggregate(ctx context.Context, platform string) (*EdgeAccountsAggregate, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		platform = PlatformAnthropic
	}
	out := &EdgeAccountsAggregate{Platform: platform, Edges: []EdgeAccountsResult{}, TS: time.Now().Unix()}
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

	sort.Slice(results, func(i, j int) bool { return results[i].EdgeID < results[j].EdgeID })
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
		targets = append(targets, edgeTarget{
			edgeID:  edgeIDFromBaseURL(baseURL),
			baseURL: baseURL,
			apiKey:  apiKey,
		})
	}
	return targets
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

// fetchEdgeAccounts GETs {base_url}/api/v1/edge/accounts?platform=... with
// x-api-key auth and an 8s timeout. Any failure → {ok:false, error}, never a
// panic and never a failed aggregate.
func (a *EdgeAccountsAggregator) fetchEdgeAccounts(ctx context.Context, t edgeTarget, platform string) EdgeAccountsResult {
	res := EdgeAccountsResult{EdgeID: t.edgeID, BaseURL: t.baseURL, Accounts: []json.RawMessage{}}
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

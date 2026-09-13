package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"

	"go.uber.org/zap"
)

// PricingAvailabilityService records per-(platform, model) verified-availability
// state and exposes a read API for pricing_catalog_tk.go BuildPublicCatalog.
//
// Design: docs/approved/pricing-availability-source-of-truth.md
//
// Protocol execution records one sample per completed forward attempt. Account
// auth/quota is inconclusive; other failures describe observations, not global
// model retirement. Only explicitly scoped, repeated provider evidence may prune.
type PricingAvailabilityService struct {
	repo  ModelAvailabilityRepository
	clock func() time.Time // injected for tests
}

// NewPricingAvailabilityService constructs the service. clock may be nil; the
// production binding wires time.Now.
func NewPricingAvailabilityService(repo ModelAvailabilityRepository, clock func() time.Time) *PricingAvailabilityService {
	if clock == nil {
		clock = time.Now
	}
	return &PricingAvailabilityService{repo: repo, clock: clock}
}

// AvailabilityOutcome is the per-request signal recorded by gateway taps and
// active probes. Fields are intentionally minimal — the service derives
// failure_kind from upstream_status_code + error body.
type AvailabilityOutcome struct {
	Platform           string
	ModelID            string
	AccountID          int64 // 0 if not applicable (active probe with no specific account)
	Success            bool
	UpstreamStatusCode int    // upstream HTTP status (0 if network error before response)
	UpstreamErrorBody  string // truncated upstream error body, used to classify model_not_found
	// ProviderModelRetired is an attestation from provider-wide discovery/review,
	// never inferred from a request on one account (including account-less probes).
	// Two observations at distinct times are required before catalog pruning.
	ProviderModelRetired bool
	NetworkError         bool // true on timeout / DNS / TLS errors (no HTTP response received)
}

// FailureKind values are the canonical taxonomy. Any new kind requires
// updating the signal classification in the approved doc.
const (
	FailureKindModelNotFound        = "model_not_found"
	FailureKindProviderModelRetired = "provider_model_retired"
	FailureKindNotFound             = "not_found"
	FailureKindRateLimited          = "rate_limited"
	FailureKindAuthFailure          = "auth_failure"
	FailureKindUpstream5xx          = "upstream_5xx"
	FailureKindNetworkError         = "network_error"
	FailureKindBadRespShape         = "bad_response_shape"
)

// AvailabilityStatus is the canonical 4-value enum mirrored in the DB.
const (
	AvailabilityStatusOK          = "ok"
	AvailabilityStatusStale       = "stale"
	AvailabilityStatusUnreachable = "unreachable"
	AvailabilityStatusUntested    = "untested"
)

// Thresholds — exposed as package vars so PR-2/PR-3 admin overrides can
// adjust them without service-level reflection.
var (
	// AvailabilityRollingWindow is the window over which sample_ok_24h /
	// sample_total_24h are accumulated. After this, counters reset to 0.
	AvailabilityRollingWindow = 24 * time.Hour

	// AvailabilityStaleAfter — last_seen_ok_at older than this flips ok→stale
	// even if the success rate is still high.
	AvailabilityStaleAfter = 24 * time.Hour

	// AvailabilityOKThreshold — 24h success rate at or above this counts ok.
	AvailabilityOKThreshold = 0.95

	// AvailabilityUnreachableThreshold — 24h success rate below this flips
	// to unreachable.
	AvailabilityUnreachableThreshold = 0.80
)

// ErrAvailabilityRepoNil indicates the service was constructed without a repo.
// Production wiring must inject one; tests may use the in-memory stub.
var ErrAvailabilityRepoNil = errors.New("pricing availability: repository is nil")

// ModelAvailabilityRepository is the persistence boundary. Backed by ent in
// production (see backend/internal/repository/model_availability_repo_tk.go in PR-1).
type ModelAvailabilityRepository interface {
	// Upsert reads the current row for (platform, model_id), applies fn, and
	// writes it back atomically. fn receives the current state (or zero-value
	// AvailabilityState if the row doesn't exist) and returns the next state.
	// Implementations must serialize concurrent calls per (platform, model_id).
	Upsert(ctx context.Context, platform, modelID string, fn func(current AvailabilityState) AvailabilityState) error

	// Get returns the current state. Caller must treat zero-value Status=""
	// as "untested / never written". Errors are propagated.
	Get(ctx context.Context, platform, modelID string) (AvailabilityState, error)
}

// modelAvailabilityBatchRepository is an optional read optimization implemented
// by the production repository. Keeping it separate preserves compatibility
// with focused test repositories while discovery can avoid one query per model.
type modelAvailabilityBatchRepository interface {
	GetBatch(ctx context.Context, platform string, modelIDs []string) (map[string]AvailabilityState, error)
}

type modelAvailabilityRequestCacheContextKey struct{}

type modelAvailabilityRequestCache struct {
	mu     sync.Mutex
	loaded map[string]struct{}
	states map[string]AvailabilityState
}

func withModelAvailabilityRequestCache(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(modelAvailabilityRequestCacheContextKey{}).(*modelAvailabilityRequestCache); ok {
		return ctx
	}
	return context.WithValue(ctx, modelAvailabilityRequestCacheContextKey{}, &modelAvailabilityRequestCache{
		loaded: make(map[string]struct{}),
		states: make(map[string]AvailabilityState),
	})
}

// AvailabilityState is the in-memory shape of a model_availability row.
// Mirrors the ent ModelAvailability fields. Pointers are used for nullable
// timestamps; a fresh state has Status="" (untested has not been written yet).
type AvailabilityState struct {
	Platform               string
	ModelID                string
	Status                 string
	LastSeenOKAt           *time.Time
	LastFailureAt          *time.Time
	LastFailureKind        string
	UpstreamStatusCodeLast *int
	LastCheckedAt          *time.Time
	SampleOK24h            int
	SampleTotal24h         int
	RollingWindowStartedAt *time.Time
	LastAccountID          *int64
}

// RecordOutcome is the single write API. Both passive taps (handlers /
// gateway) and active probes call this; the failure-classification matrix
// (§1.3) is implemented here so both paths share semantics.
func (s *PricingAvailabilityService) RecordOutcome(ctx context.Context, outcome AvailabilityOutcome) {
	if s == nil || s.repo == nil {
		return
	}
	platform := strings.TrimSpace(outcome.Platform)
	model := strings.TrimSpace(outcome.ModelID)
	if platform == "" || model == "" {
		return
	}

	now := s.clock().UTC()

	err := s.repo.Upsert(ctx, platform, model, func(cur AvailabilityState) AvailabilityState {
		next := cur
		next.Platform = platform
		next.ModelID = model
		next.LastCheckedAt = availabilityPtrTime(now)
		if outcome.AccountID != 0 {
			next.LastAccountID = availabilityPtrInt64(outcome.AccountID)
		}
		if outcome.UpstreamStatusCode != 0 {
			next.UpstreamStatusCodeLast = availabilityPtrInt(outcome.UpstreamStatusCode)
		}

		// Roll the 24h window if needed (after computing now, before mutating
		// counters). On reset, the previous window's totals are discarded —
		// status derivation always uses the CURRENT window.
		next = rollWindowIfStale(next, now)

		switch {
		case outcome.Success:
			next = applySuccess(next, now)

		default:
			kind := classifyFailureKind(outcome)
			if kind == FailureKindModelNotFound && outcome.ProviderModelRetired && outcome.AccountID == 0 {
				kind = FailureKindProviderModelRetired
			}
			next.LastFailureKind = kind
			next.LastFailureAt = availabilityPtrTime(now)

			switch kind {
			case FailureKindRateLimited, FailureKindAuthFailure:
				// INCONCLUSIVE: account-level signal, not model-level. Do not
				// pollute sample counts; only the last_checked_at refresh
				// (above) prevents the seeder from reprobing too eagerly.
				// Status remains whatever it was.
			case FailureKindProviderModelRetired:
				// The persistent kind separates an attested provider observation from
				// legacy account 404 rows. Never promote accumulated request failures.
				next.SampleTotal24h++
				next.LastAccountID = nil
				next.Status = AvailabilityStatusStale
				if cur.LastFailureKind == kind && cur.LastFailureAt != nil &&
					(now.After(*cur.LastFailureAt) || cur.Status == AvailabilityStatusUnreachable) &&
					!now.Before(*cur.LastFailureAt) && now.Sub(*cur.LastFailureAt) < AvailabilityRollingWindow {
					next.Status = AvailabilityStatusUnreachable
				}
			default:
				// not_found / upstream_5xx / network_error / bad_response_shape:
				// soft accumulators. Re-derive status from rolling counters.
				next.SampleTotal24h = next.SampleTotal24h + 1
				next.Status = deriveStatus(next, now)
			}
		}

		return next
	})

	if err != nil {
		// Best-effort write; never block the request path.
		logger.FromContext(ctx).Warn("pricing.availability.record_failed",
			zap.String("platform", platform),
			zap.String("model", model),
			zap.Error(err))
	}
}

// GetAvailability is the read API used by pricing_catalog_tk.go to inject
// `availability` into the public catalog response. Returns zero-value
// AvailabilityState (Status="") if the cell has never been written;
// callers should map that to `untested` in the response shape.
func (s *PricingAvailabilityService) GetAvailability(ctx context.Context, platform, modelID string) (AvailabilityState, error) {
	if s == nil || s.repo == nil {
		return AvailabilityState{}, ErrAvailabilityRepoNil
	}
	state, err := s.repo.Get(ctx, strings.TrimSpace(platform), strings.TrimSpace(modelID))
	if err != nil {
		return AvailabilityState{}, err
	}
	return availabilityStateAt(state, s.clock().UTC()), nil
}

// GetAvailabilityBatch returns the current states for the requested model IDs.
// Missing rows are omitted and therefore retain the same zero-value/untested
// semantics as GetAvailability.
func (s *PricingAvailabilityService) GetAvailabilityBatch(ctx context.Context, platform string, modelIDs []string) (map[string]AvailabilityState, error) {
	if s == nil || s.repo == nil {
		return nil, ErrAvailabilityRepoNil
	}
	platform = strings.TrimSpace(platform)
	unique := make([]string, 0, len(modelIDs))
	seen := make(map[string]struct{}, len(modelIDs))
	for _, modelID := range modelIDs {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			continue
		}
		if _, ok := seen[modelID]; ok {
			continue
		}
		seen[modelID] = struct{}{}
		unique = append(unique, modelID)
	}
	if len(unique) == 0 {
		return map[string]AvailabilityState{}, nil
	}
	var states map[string]AvailabilityState
	var err error
	if cache, ok := ctx.Value(modelAvailabilityRequestCacheContextKey{}).(*modelAvailabilityRequestCache); ok && cache != nil {
		states, err = s.getAvailabilityBatchCached(ctx, cache, platform, unique)
	} else {
		states, err = s.getAvailabilityBatchUncached(ctx, platform, unique)
	}
	if err != nil {
		return nil, err
	}
	// Cache raw evidence, then derive at read time. Even a cache hit crossing
	// the window boundary must not keep an expired badge or sample count.
	now := s.clock().UTC()
	out := make(map[string]AvailabilityState, len(states))
	for modelID, state := range states {
		out[modelID] = availabilityStateAt(state, now)
	}
	return out, nil
}

func (s *PricingAvailabilityService) getAvailabilityBatchCached(
	ctx context.Context,
	cache *modelAvailabilityRequestCache,
	platform string,
	modelIDs []string,
) (map[string]AvailabilityState, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	missing := make([]string, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		if _, ok := cache.loaded[platform+"\x00"+modelID]; !ok {
			missing = append(missing, modelID)
		}
	}
	if len(missing) > 0 {
		loaded, err := s.getAvailabilityBatchUncached(ctx, platform, missing)
		if err != nil {
			return nil, err
		}
		for _, modelID := range missing {
			key := platform + "\x00" + modelID
			cache.loaded[key] = struct{}{}
			if state, ok := loaded[modelID]; ok {
				cache.states[key] = state
			}
		}
	}

	out := make(map[string]AvailabilityState, len(modelIDs))
	for _, modelID := range modelIDs {
		if state, ok := cache.states[platform+"\x00"+modelID]; ok {
			out[modelID] = state
		}
	}
	return out, nil
}

func (s *PricingAvailabilityService) getAvailabilityBatchUncached(ctx context.Context, platform string, modelIDs []string) (map[string]AvailabilityState, error) {
	if batchRepo, ok := s.repo.(modelAvailabilityBatchRepository); ok {
		return batchRepo.GetBatch(ctx, platform, modelIDs)
	}
	out := make(map[string]AvailabilityState, len(modelIDs))
	for _, modelID := range modelIDs {
		state, err := s.repo.Get(ctx, platform, modelID)
		if err != nil {
			return nil, err
		}
		if state.ModelID != "" || state.Status != "" {
			out[modelID] = state
		}
	}
	return out, nil
}

// SuccessRate24h is a small helper for callers (catalog handler, frontend
// derivation, tests). Returns 0 when sample_total_24h is 0.
func (a AvailabilityState) SuccessRate24h() float64 {
	if a.SampleTotal24h <= 0 {
		return 0
	}
	return float64(a.SampleOK24h) / float64(a.SampleTotal24h)
}

// --- internal helpers ---

// availabilityStateAt projects time-bounded evidence without changing stored
// counters or observation timestamps. An empty row remains never-tested.
func availabilityStateAt(s AvailabilityState, now time.Time) AvailabilityState {
	if s == (AvailabilityState{}) {
		return s
	}
	if s.RollingWindowStartedAt == nil || now.Sub(*s.RollingWindowStartedAt) >= AvailabilityRollingWindow {
		s.SampleOK24h, s.SampleTotal24h = 0, 0
	}
	if s.LastFailureKind == FailureKindProviderModelRetired {
		// Reads cannot promote a first attestation or renew expired proof.
		if !tkAvailabilityStructurallyGone(s) || s.LastFailureAt == nil || now.Sub(*s.LastFailureAt) >= AvailabilityRollingWindow {
			s.Status = AvailabilityStatusStale
		}
		return s
	}
	s.Status = deriveStatus(s, now)
	return s
}

func applySuccess(s AvailabilityState, now time.Time) AvailabilityState {
	s.SampleOK24h = s.SampleOK24h + 1
	s.SampleTotal24h = s.SampleTotal24h + 1
	s.LastSeenOKAt = availabilityPtrTime(now)
	s.LastFailureKind = ""
	s.LastFailureAt = nil
	s.Status = deriveStatus(s, now)
	return s
}

func rollWindowIfStale(s AvailabilityState, now time.Time) AvailabilityState {
	if s.RollingWindowStartedAt == nil || now.Sub(*s.RollingWindowStartedAt) >= AvailabilityRollingWindow {
		s.SampleOK24h = 0
		s.SampleTotal24h = 0
		s.RollingWindowStartedAt = availabilityPtrTime(now)
	}
	return s
}

// deriveStatus computes the canonical 4-value status from rolling window
// counters + last_seen_ok_at. Only attested provider retirement uses a separate
// repeated-evidence decision; request-level model-not-found is a soft sample.
func deriveStatus(s AvailabilityState, now time.Time) string {
	if s.SampleTotal24h <= 0 {
		if s.LastSeenOKAt != nil || s.LastFailureAt != nil {
			return AvailabilityStatusStale
		}
		return AvailabilityStatusUntested
	}
	rate := s.SuccessRate24h()
	switch {
	case rate >= AvailabilityOKThreshold && s.LastSeenOKAt != nil && now.Sub(*s.LastSeenOKAt) < AvailabilityStaleAfter:
		return AvailabilityStatusOK
	case rate < AvailabilityUnreachableThreshold:
		return AvailabilityStatusUnreachable
	default:
		return AvailabilityStatusStale
	}
}

// classifyFailureKind walks §1.3 matrix. Order matters: more-specific
// substring checks before generic status-code buckets.
func classifyFailureKind(o AvailabilityOutcome) string {
	if o.NetworkError {
		return FailureKindNetworkError
	}
	body := strings.ToLower(o.UpstreamErrorBody)
	switch {
	case o.UpstreamStatusCode == 429 ||
		strings.Contains(body, "rate limit") ||
		strings.Contains(body, "rate_limit") ||
		strings.Contains(body, "quota"):
		return FailureKindRateLimited
	case o.UpstreamStatusCode == 401 || o.UpstreamStatusCode == 403:
		return FailureKindAuthFailure
	case o.UpstreamStatusCode >= 400 && o.UpstreamStatusCode < 500 &&
		( // Google Code Assist / generativelanguage 标准 model-not-found body
		strings.Contains(body, "requested entity was not found") ||
			// Anthropic / OpenAI 风格 explicit not_found markers
			strings.Contains(body, "not_found_error") ||
			// Codex 形态："The 'X' model is not supported when using Codex..."
			(strings.Contains(body, "model") && strings.Contains(body, "not supported")) ||
			// 通用 "model ... not found" / "model not found"
			(strings.Contains(body, "model") &&
				(strings.Contains(body, "not found") || strings.Contains(body, "not_found") || strings.Contains(body, "retired")))):
		return FailureKindModelNotFound
	case o.UpstreamStatusCode == 404:
		return FailureKindNotFound
	case o.UpstreamStatusCode >= 500 && o.UpstreamStatusCode < 600:
		return FailureKindUpstream5xx
	case o.UpstreamStatusCode == 200:
		// success was false but status was 200 — bad shape (e.g. JSON parse error)
		return FailureKindBadRespShape
	}
	return FailureKindUpstream5xx
}

// pointer helpers — Go-idiomatic nil-safety for nullable timestamp fields.
// Named with availability* prefix to avoid collisions with package-wide
// ptr helpers in other test files.
func availabilityPtrTime(t time.Time) *time.Time { return &t }
func availabilityPtrInt(i int) *int              { return &i }
func availabilityPtrInt64(i int64) *int64        { return &i }

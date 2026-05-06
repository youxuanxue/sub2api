package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"

	"go.uber.org/zap"
)

// PricingAvailabilityService records per-(platform, model) verified-availability
// state and exposes a read API for pricing_catalog_tk.go BuildPublicCatalog.
//
// Design: docs/approved/pricing-availability-source-of-truth.md
//
// Population (PR-1): passive only — gateway forward path success/failure
// emits RecordOutcome via 1-line hook in gateway_service.go recordUsageCore
// + 3 handler taps. Active probes (PR-2) reuse ChannelMonitorRunner with
// kind=system_availability rows.
//
// Failure-classification matrix (§1.3 of the approved doc) is the single
// place that decides whether an upstream error reflects on the model
// (model_not_found / not_found / 5xx / network) or on the account
// (rate_limited / auth_failure). Account-level failures DO NOT pollute
// sample counts — they only refresh last_checked_at to avoid reprobing
// soon. This invariant is co-implemented with Account.RateLimitService;
// do not duplicate cooldown logic here.
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
	NetworkError       bool   // true on timeout / DNS / TLS errors (no HTTP response received)
}

// FailureKind values are the canonical taxonomy. Any new kind requires
// updating §1.3 of the approved doc.
const (
	FailureKindModelNotFound = "model_not_found"
	FailureKindNotFound      = "not_found"
	FailureKindRateLimited   = "rate_limited"
	FailureKindAuthFailure   = "auth_failure"
	FailureKindUpstream5xx   = "upstream_5xx"
	FailureKindNetworkError  = "network_error"
	FailureKindBadRespShape  = "bad_response_shape"
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
			next.LastFailureKind = kind
			next.LastFailureAt = availabilityPtrTime(now)

			switch kind {
			case FailureKindRateLimited, FailureKindAuthFailure:
				// INCONCLUSIVE: account-level signal, not model-level. Do not
				// pollute sample counts; only the last_checked_at refresh
				// (above) prevents the seeder from reprobing too eagerly.
				// Status remains whatever it was.
			case FailureKindModelNotFound:
				// STRONG signal: single sample is enough.
				next.SampleTotal24h = next.SampleTotal24h + 1
				next.Status = AvailabilityStatusUnreachable
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
	return s.repo.Get(ctx, strings.TrimSpace(platform), strings.TrimSpace(modelID))
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
// counters + last_seen_ok_at. ModelNotFound is handled at the call site
// (it short-circuits to unreachable before deriveStatus is consulted).
func deriveStatus(s AvailabilityState, now time.Time) string {
	if s.SampleTotal24h <= 0 && s.LastSeenOKAt == nil {
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
				(strings.Contains(body, "not found") || strings.Contains(body, "not_found")))):
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

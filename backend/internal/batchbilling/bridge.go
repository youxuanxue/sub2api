// Package batchbilling is the narrow billing/security seam for Managed Batch.
//
// It owns admission and outcome policy, while the existing usage/billing owner
// remains responsible for pricing, quota mutation and usage-log persistence.
// The package deliberately has no database, queue or HTTP dependency.
package batchbilling

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

var (
	ErrInvalidOwner       = errors.New("batch billing: invalid owner")
	ErrInvalidRequest     = errors.New("batch billing: invalid request")
	ErrAdmissionDenied    = errors.New("batch billing: admission denied")
	ErrOwnerNotFound      = errors.New("batch billing: batch not found")
	ErrSettlementConflict = errors.New("batch billing: settlement idempotency conflict")
	ErrUnknownUnresolved  = errors.New("batch billing: unknown outcome requires reconciliation")
	ErrInvalidAudit       = errors.New("batch billing: invalid audit fields")
)

// Owner is the authenticated visibility scope. APIKeyID=0 means the user
// scope is intentionally not bound to one key; a non-zero key is checked too.
type Owner struct {
	UserID   int64
	APIKeyID int64
}

func (o Owner) Valid() bool { return o.UserID > 0 && o.APIKeyID >= 0 }

// Same reports whether two owners can see the same batch.
func (o Owner) Same(other Owner) bool {
	return o.Valid() && other.Valid() && o.UserID == other.UserID &&
		(o.APIKeyID == 0 || other.APIKeyID == 0 || o.APIKeyID == other.APIKeyID)
}

type Limits struct {
	MaxItems         int
	MaxInputBytes    int64
	MaxActiveBatches int
	MaxActiveItems   int
}

func (l Limits) validate() error {
	if l.MaxItems <= 0 || l.MaxInputBytes <= 0 || l.MaxActiveBatches <= 0 || l.MaxActiveItems <= 0 {
		return fmt.Errorf("%w: limits must be positive", ErrInvalidRequest)
	}
	return nil
}

type ActiveCounts struct {
	Batches int
	Items   int
}

type AdmissionRequest struct {
	Owner      Owner
	BatchID    string
	ItemCount  int
	InputBytes int64
}

type AdmissionDecision struct {
	Owner      Owner
	BatchID    string
	ItemCount  int
	InputBytes int64
}

// AdmissionCounter is implemented by persistence. It must count only active
// batches/items in the supplied owner scope and must not accept a caller-wide
// unscoped query.
type AdmissionCounter interface {
	ActiveCounts(context.Context, Owner) (ActiveCounts, error)
}

type AdmissionController struct {
	Counter AdmissionCounter
	Limits  Limits
}

func (c *AdmissionController) Admit(ctx context.Context, req AdmissionRequest) (AdmissionDecision, error) {
	if c == nil || c.Counter == nil {
		return AdmissionDecision{}, fmt.Errorf("%w: missing counter", ErrInvalidRequest)
	}
	if err := c.Limits.validate(); err != nil {
		return AdmissionDecision{}, err
	}
	req.BatchID = strings.TrimSpace(req.BatchID)
	if !req.Owner.Valid() || req.BatchID == "" || req.ItemCount <= 0 || req.InputBytes < 0 {
		return AdmissionDecision{}, fmt.Errorf("%w: malformed admission request", ErrInvalidRequest)
	}
	if req.ItemCount > c.Limits.MaxItems || req.InputBytes > c.Limits.MaxInputBytes {
		return AdmissionDecision{}, fmt.Errorf("%w: batch size exceeds limit", ErrAdmissionDenied)
	}
	counts, err := c.Counter.ActiveCounts(ctx, req.Owner)
	if err != nil {
		return AdmissionDecision{}, err
	}
	if counts.Batches < 0 || counts.Items < 0 {
		return AdmissionDecision{}, fmt.Errorf("%w: invalid active counts", ErrInvalidRequest)
	}
	if counts.Batches >= c.Limits.MaxActiveBatches || req.ItemCount > c.Limits.MaxActiveItems-counts.Items {
		return AdmissionDecision{}, fmt.Errorf("%w: active batch limit reached", ErrAdmissionDenied)
	}
	return AdmissionDecision(req), nil
}

// BatchOwnerReader is the persistence owner for access checks. Implementations
// should return a not-found error for an absent batch without revealing whether
// another owner has a matching ID.
type BatchOwnerReader interface {
	BatchOwner(context.Context, string) (Owner, error)
}

type AccessGuard struct{ Reader BatchOwnerReader }

func (g AccessGuard) Require(ctx context.Context, owner Owner, batchID string) error {
	if !owner.Valid() || strings.TrimSpace(batchID) == "" || g.Reader == nil {
		return ErrOwnerNotFound
	}
	stored, err := g.Reader.BatchOwner(ctx, strings.TrimSpace(batchID))
	if err != nil || !stored.Same(owner) {
		// Collapse missing and cross-owner records into one externally safe result.
		return ErrOwnerNotFound
	}
	return nil
}

// ItemRef identifies the terminal attempt whose billing decision is being
// recorded. The key is stable across retries and is the sole settlement
// idempotency key; provider request IDs never enter it.
type ItemRef struct {
	BatchID string
	ItemID  string
	Attempt int
}

func (r ItemRef) valid() bool {
	return strings.TrimSpace(r.BatchID) != "" && strings.TrimSpace(r.ItemID) != "" && r.Attempt > 0
}

func SettlementKey(ref ItemRef) (string, error) {
	if !ref.valid() {
		return "", fmt.Errorf("%w: invalid item reference", ErrInvalidRequest)
	}
	return fmt.Sprintf("batch/%s/item/%s/attempt/%d", strings.TrimSpace(ref.BatchID), strings.TrimSpace(ref.ItemID), ref.Attempt), nil
}

func HoldKey(ref ItemRef) (string, error) {
	k, err := SettlementKey(ref)
	if err != nil {
		return "", err
	}
	return "hold/" + k, nil
}

type UsageObservation struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

func (u UsageObservation) Valid() bool {
	return u.InputTokens >= 0 && u.OutputTokens >= 0 && u.TotalTokens == u.InputTokens+u.OutputTokens
}

type SettlementRequest struct {
	Owner       Owner
	Ref         ItemRef
	Usage       UsageObservation
	RequestHash string
}

type SettlementResult struct {
	Applied bool
}

// SettlementOwner is an adapter over the existing usage/billing owner. It
// must calculate price and mutate balance/quota through that owner; this seam
// never receives a price table or derives cost from token counts.
type SettlementOwner interface {
	Settle(context.Context, SettlementRequest, string) (SettlementResult, error)
}

type HoldOwner interface {
	Reserve(context.Context, Owner, string, float64) (bool, error)
	Release(context.Context, Owner, string) (bool, error)
}

// Bridge composes the admission, hold and settlement seams. It contains no
// pricing logic: the supplied SettlementOwner remains the single billing
// owner and receives the stable hold key for atomic consumption.
type Bridge struct {
	Admission  *AdmissionController
	Holds      HoldOwner
	Settlement SettlementOwner
}

func (b *Bridge) ReserveItem(ctx context.Context, owner Owner, ref ItemRef, amount float64) (bool, error) {
	if b == nil || b.Holds == nil || !owner.Valid() || !ValidAmount(amount) || amount <= 0 {
		return false, fmt.Errorf("%w: invalid hold request", ErrInvalidRequest)
	}
	key, err := HoldKey(ref)
	if err != nil {
		return false, err
	}
	return b.Holds.Reserve(ctx, owner, key, amount)
}

func (b *Bridge) ReleaseItem(ctx context.Context, owner Owner, ref ItemRef) (bool, error) {
	if b == nil || b.Holds == nil || !owner.Valid() {
		return false, fmt.Errorf("%w: invalid release request", ErrInvalidRequest)
	}
	key, err := HoldKey(ref)
	if err != nil {
		return false, err
	}
	return b.Holds.Release(ctx, owner, key)
}

func (b *Bridge) SettleItem(ctx context.Context, req SettlementRequest) (SettlementResult, error) {
	if b == nil || b.Settlement == nil || !req.Owner.Valid() || !req.Ref.valid() || !req.Usage.Valid() {
		return SettlementResult{}, fmt.Errorf("%w: invalid settlement request", ErrInvalidRequest)
	}
	key, err := HoldKey(req.Ref)
	if err != nil {
		return SettlementResult{}, err
	}
	return b.Settlement.Settle(ctx, req, key)
}

type Outcome string

const (
	OutcomeSettled  Outcome = "settled"
	OutcomeReleased Outcome = "released"
)

type UnknownDecision struct {
	Ref           ItemRef
	Outcome       Outcome
	Usage         UsageObservation
	UsageObserved bool
	RequestHash   string
}

// Reconciler resolves only explicit decisions. Unknown is never treated as a
// transient failure and is never replayed by this package.
type Reconciler struct {
	Settlement SettlementOwner
	Holds      HoldOwner
}

func (r *Reconciler) ResolveUnknown(ctx context.Context, req SettlementRequest, decision UnknownDecision) (SettlementResult, error) {
	if r == nil || !req.Owner.Valid() || !req.Ref.valid() || decision.Ref != req.Ref {
		return SettlementResult{}, fmt.Errorf("%w: malformed reconciliation request", ErrInvalidRequest)
	}
	if decision.Outcome == "" {
		return SettlementResult{}, ErrUnknownUnresolved
	}
	holdKey, err := HoldKey(req.Ref)
	if err != nil {
		return SettlementResult{}, err
	}
	switch decision.Outcome {
	case OutcomeSettled:
		if r.Settlement == nil || !decision.UsageObserved || !decision.Usage.Valid() {
			return SettlementResult{}, fmt.Errorf("%w: settled requires observed usage", ErrUnknownUnresolved)
		}
		if decision.RequestHash == "" {
			decision.RequestHash = req.RequestHash
		}
		return r.Settlement.Settle(ctx, SettlementRequest{Owner: req.Owner, Ref: req.Ref, Usage: decision.Usage, RequestHash: decision.RequestHash}, holdKey)
	case OutcomeReleased:
		if decision.UsageObserved {
			return SettlementResult{}, fmt.Errorf("%w: observed usage must be settled", ErrUnknownUnresolved)
		}
		if r.Holds == nil {
			return SettlementResult{}, fmt.Errorf("%w: hold owner unavailable", ErrInvalidRequest)
		}
		_, err := r.Holds.Release(ctx, req.Owner, holdKey)
		return SettlementResult{Applied: err == nil}, err
	default:
		return SettlementResult{}, fmt.Errorf("%w: unsupported decision", ErrUnknownUnresolved)
	}
}

// AuditFields is the only shape permitted in batch audit events. It excludes
// prompts, responses, token contents, credentials and provider request bodies.
type AuditFields map[string]string

var auditKeys = map[string]struct{}{
	"batch_id": {}, "item_id": {}, "attempt": {}, "owner_user_id": {},
	"status": {}, "outcome": {}, "error_code": {}, "provider": {},
}

func SanitizeAuditFields(fields map[string]string) (AuditFields, error) {
	if fields == nil {
		return AuditFields{}, nil
	}
	out := make(AuditFields, len(fields))
	for key, value := range fields {
		if _, ok := auditKeys[key]; !ok || strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("%w: key %q is not allowed", ErrInvalidAudit, key)
		}
		if key == "owner_user_id" {
			for _, r := range value {
				if r < '0' || r > '9' {
					return nil, fmt.Errorf("%w: owner_user_id must be numeric", ErrInvalidAudit)
				}
			}
		}
		if len(value) > 256 {
			return nil, fmt.Errorf("%w: value too long", ErrInvalidAudit)
		}
		out[key] = value
	}
	return out, nil
}

func SortedAuditKeys(fields AuditFields) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ValidAmount(amount float64) bool {
	return amount >= 0 && !math.IsNaN(amount) && !math.IsInf(amount, 0)
}

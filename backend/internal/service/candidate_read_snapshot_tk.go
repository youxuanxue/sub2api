package service

// Candidate read snapshots are a shadow/read-model boundary for candidate
// routing.  They deliberately contain only immutable, non-secret facts.  The
// existing candidate selector remains the decision owner until a later rollout.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

var (
	ErrCandidateReadSnapshotIncomplete   = errors.New("candidate read snapshot is incomplete")
	ErrCandidateReadSnapshotInvalid      = errors.New("candidate read snapshot is invalid")
	ErrCandidateReadSnapshotLagged       = errors.New("candidate read snapshot is lagged")
	ErrCandidateReadSnapshotUnavailable  = errors.New("candidate read snapshot is unavailable")
	ErrCandidateReadSnapshotFallbackBusy = errors.New("candidate read snapshot fallback is busy")
)

// CandidateReadSnapshot is a complete generation of static candidate facts.
// Runtime readiness, credentials, quota, cooldown and billing state are
// intentionally absent.  Maps are owned by the provider after publication and
// must be treated as immutable by callers.
type CandidateReadSnapshot struct {
	ReadRevision    uint64
	SourceWatermark uint64
	PublishedAt     time.Time
	Complete        bool
	Accounts        map[int64]CandidateAccountFacts
	Capabilities    map[string]CandidateCapabilityFacts
	Groups          map[int64]CandidateGroupFacts
	Memberships     map[int64][]CandidateMembership
	Indexes         CandidateIndexes
}

type CandidateAccountFacts struct {
	ID              int64
	Platform        string
	CapabilityKey   string
	ModelMapping    ModelMappingProjection
	AccountGroupIDs []int64
}

// ModelMappingProjection is the non-secret model alias projection used for
// static prefiltering. It intentionally excludes the account credential map.
type ModelMappingProjection = map[string]string

// CandidateCapabilityFacts is shared by accounts with the same canonical
// capability identity. EndpointPolicyDigest is a digest so endpoint URLs and
// other endpoint metadata never enter this read model.
type CandidateCapabilityFacts struct {
	CapabilityKey        string
	NativeProtocols      []protocolrouter.Protocol
	EndpointIdentity     EndpointIdentityProjection
	EndpointPolicyDigest string
	Revision             int64
}

// EndpointIdentityProjection omits endpoint URLs and credentials. The
// capability key remains the canonical identity used by Plan/revalidation.
type EndpointIdentityProjection struct {
	Platform               string
	EndpointProfile        string
	ChannelType            string
	UpstreamRequestProfile string
}

type CandidateGroupFacts struct {
	ID             int64
	Active         bool
	Platform       string
	ModelAllowlist GroupModelAllowlist
	EndpointPolicy GroupEndpointPolicy
	DirectPolicy   CandidateDirectPolicyProjection
}

type GroupEndpointPolicy struct {
	RequirePrivacySet bool
	RequireOAuthOnly  bool
}

type CandidateDirectPolicyProjection struct {
	Platform           string
	DefaultMappedModel string
	ModelAllowlist     GroupModelAllowlist
}

// DirectCandidatePolicyProjection is the design-document name retained as an
// alias so callers cannot accidentally create a second policy representation.
type DirectCandidatePolicyProjection = CandidateDirectPolicyProjection

type CandidateMembership struct {
	AccountID int64
	Priority  int
}

type CandidateIndexes struct {
	AccountsByGroup    map[int64][]int64
	AccountsByPlatform map[string][]int64
}

// CandidateReadSnapshotProvider is the single read owner used by shadow
// consumers. A returned snapshot is either complete and scoped to groupIDs or
// an error; incomplete data is never an authorization grant.
type CandidateReadSnapshotProvider interface {
	Snapshot(context.Context, []int64) (CandidateReadSnapshot, error)
}

// CandidateReadSnapshotStore is the materializer-facing extension of the
// provider boundary. Request code should depend only on
// CandidateReadSnapshotProvider; publication and lag state stay with the
// snapshot owner.
type CandidateReadSnapshotStore interface {
	CandidateReadSnapshotProvider
	PublishCandidateReadSnapshot(CandidateReadSnapshot) error
	MarkCandidateReadSnapshotLagged()
	Stats() CandidateReadSnapshotProviderStats
}

// CandidateReadSnapshotFallback is a bounded batch repository read. It must
// hydrate all requested groups in one operation and must not issue per-group
// queries from the request goroutine.
type CandidateReadSnapshotFallback interface {
	BuildCandidateReadSnapshot(context.Context, []int64) (CandidateReadSnapshot, error)
}

type CandidateReadSnapshotFallbackFunc func(context.Context, []int64) (CandidateReadSnapshot, error)

func (f CandidateReadSnapshotFallbackFunc) BuildCandidateReadSnapshot(ctx context.Context, ids []int64) (CandidateReadSnapshot, error) {
	return f(ctx, ids)
}

// CandidateReadSnapshotProviderStats is a point-in-time diagnostic view. It
// is intentionally local and cheap; production metric wiring can consume it
// without changing the provider's correctness contract.
type CandidateReadSnapshotProviderStats struct {
	Hits             uint64
	Fallbacks        uint64
	FallbackBusy     uint64
	InvalidFallbacks uint64
	Lagged           uint64
}

type candidateReadSnapshotProvider struct {
	mu               sync.RWMutex
	current          CandidateReadSnapshot
	published        bool
	lagged           bool
	fallback         CandidateReadSnapshotFallback
	limit            chan struct{}
	hits             atomic.Uint64
	fallbacks        atomic.Uint64
	fallbackBusy     atomic.Uint64
	invalidFallbacks atomic.Uint64
	laggedReads      atomic.Uint64
}

// NewCandidateReadSnapshotProvider constructs a provider in shadow-only mode.
// fallbackLimit bounds concurrent repository rebuilds; values <= 0 disable
// fallback rather than allowing an unbounded database storm.
func NewCandidateReadSnapshotProvider(fallback CandidateReadSnapshotFallback, fallbackLimit int) CandidateReadSnapshotStore {
	return newCandidateReadSnapshotProvider(fallback, fallbackLimit)
}

func newCandidateReadSnapshotProvider(fallback CandidateReadSnapshotFallback, fallbackLimit int) *candidateReadSnapshotProvider {
	if fallbackLimit < 0 {
		fallbackLimit = 0
	}
	p := &candidateReadSnapshotProvider{fallback: fallback}
	if fallbackLimit > 0 {
		p.limit = make(chan struct{}, fallbackLimit)
	}
	return p
}

// PublishCandidateReadSnapshot atomically publishes a complete generation.
// Invalid or incomplete generations are rejected and cannot replace the last
// complete generation.
func (p *candidateReadSnapshotProvider) PublishCandidateReadSnapshot(snapshot CandidateReadSnapshot) error {
	if p == nil {
		return ErrCandidateReadSnapshotUnavailable
	}
	if err := ValidateCandidateReadSnapshot(snapshot); err != nil {
		p.mu.Lock()
		if p.published {
			p.lagged = true
		}
		p.mu.Unlock()
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.published && snapshot.ReadRevision <= p.current.ReadRevision {
		p.lagged = true
		return fmt.Errorf("%w: revision %d is not newer than %d", ErrCandidateReadSnapshotInvalid, snapshot.ReadRevision, p.current.ReadRevision)
	}
	if p.published && p.current.SourceWatermark > 0 && snapshot.SourceWatermark > 0 && snapshot.SourceWatermark < p.current.SourceWatermark {
		p.lagged = true
		return fmt.Errorf("%w: source watermark %d regressed from %d", ErrCandidateReadSnapshotInvalid, snapshot.SourceWatermark, p.current.SourceWatermark)
	}
	p.current = cloneCandidateReadSnapshot(snapshot)
	p.published = true
	p.lagged = false
	return nil
}

// MarkCandidateReadSnapshotLagged retains the previous complete generation,
// but prevents it from being used as proof of current authorization. A
// successful fallback clears the degraded state for that request only.
func (p *candidateReadSnapshotProvider) MarkCandidateReadSnapshotLagged() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.lagged = true
	p.mu.Unlock()
}

func (p *candidateReadSnapshotProvider) Snapshot(ctx context.Context, groupIDs []int64) (CandidateReadSnapshot, error) {
	if p == nil {
		return CandidateReadSnapshot{}, ErrCandidateReadSnapshotUnavailable
	}
	ids := normalizePositiveIDs(groupIDs)
	p.mu.RLock()
	snapshot, published, lagged := p.current, p.published, p.lagged
	p.mu.RUnlock()
	if published && !lagged {
		if scoped, ok := scopeCandidateReadSnapshot(snapshot, ids); ok {
			p.hits.Add(1)
			return scoped, nil
		}
	}
	if lagged {
		p.laggedReads.Add(1)
	}
	if p.fallback == nil || p.limit == nil {
		if lagged {
			return CandidateReadSnapshot{}, ErrCandidateReadSnapshotLagged
		}
		return CandidateReadSnapshot{}, ErrCandidateReadSnapshotUnavailable
	}
	select {
	case p.limit <- struct{}{}:
		defer func() { <-p.limit }()
	case <-ctx.Done():
		return CandidateReadSnapshot{}, ctx.Err()
	default:
		p.fallbackBusy.Add(1)
		return CandidateReadSnapshot{}, ErrCandidateReadSnapshotFallbackBusy
	}
	p.fallbacks.Add(1)
	loaded, err := p.fallback.BuildCandidateReadSnapshot(ctx, ids)
	if err != nil {
		return CandidateReadSnapshot{}, fmt.Errorf("candidate read snapshot fallback: %w", err)
	}
	if err := ValidateCandidateReadSnapshot(loaded); err != nil {
		p.invalidFallbacks.Add(1)
		return CandidateReadSnapshot{}, fmt.Errorf("candidate read snapshot fallback: %w", err)
	}
	if scoped, ok := scopeCandidateReadSnapshot(loaded, ids); ok {
		return scoped, nil
	}
	return CandidateReadSnapshot{}, ErrCandidateReadSnapshotIncomplete
}

func (p *candidateReadSnapshotProvider) Stats() CandidateReadSnapshotProviderStats {
	if p == nil {
		return CandidateReadSnapshotProviderStats{}
	}
	return CandidateReadSnapshotProviderStats{
		Hits: p.hits.Load(), Fallbacks: p.fallbacks.Load(), FallbackBusy: p.fallbackBusy.Load(),
		InvalidFallbacks: p.invalidFallbacks.Load(), Lagged: p.laggedReads.Load(),
	}
}

// ValidateCandidateReadSnapshot verifies structural completeness. It does not
// validate runtime readiness or protocol legality; those remain existing SSOT
// owners.
func ValidateCandidateReadSnapshot(snapshot CandidateReadSnapshot) error {
	if !snapshot.Complete {
		return ErrCandidateReadSnapshotIncomplete
	}
	if snapshot.ReadRevision == 0 {
		return fmt.Errorf("%w: read revision is required", ErrCandidateReadSnapshotInvalid)
	}
	for id, account := range snapshot.Accounts {
		if id <= 0 || account.ID != id {
			return fmt.Errorf("%w: account key %d", ErrCandidateReadSnapshotInvalid, id)
		}
		for _, groupID := range account.AccountGroupIDs {
			if groupID <= 0 {
				return fmt.Errorf("%w: account %d has invalid group %d", ErrCandidateReadSnapshotInvalid, id, groupID)
			}
			if _, ok := snapshot.Groups[groupID]; !ok {
				return fmt.Errorf("%w: account %d references missing group %d", ErrCandidateReadSnapshotInvalid, id, groupID)
			}
		}
		if account.CapabilityKey != "" {
			if _, ok := snapshot.Capabilities[account.CapabilityKey]; !ok {
				return fmt.Errorf("%w: account %d references missing capability %q", ErrCandidateReadSnapshotInvalid, id, account.CapabilityKey)
			}
		}
	}
	for key, capability := range snapshot.Capabilities {
		if strings.TrimSpace(key) == "" || capability.CapabilityKey != key {
			return fmt.Errorf("%w: capability key %q", ErrCandidateReadSnapshotInvalid, key)
		}
		for _, protocol := range capability.NativeProtocols {
			if !protocol.Valid() {
				return fmt.Errorf("%w: capability %q has invalid protocol %q", ErrCandidateReadSnapshotInvalid, key, protocol)
			}
		}
	}
	for id, group := range snapshot.Groups {
		if id <= 0 || group.ID != id {
			return fmt.Errorf("%w: group key %d", ErrCandidateReadSnapshotInvalid, id)
		}
		if _, ok := snapshot.Memberships[id]; !ok {
			return fmt.Errorf("%w: group %d has no membership bucket", ErrCandidateReadSnapshotInvalid, id)
		}
	}
	for groupID, members := range snapshot.Memberships {
		if _, ok := snapshot.Groups[groupID]; !ok {
			return fmt.Errorf("%w: membership references missing group %d", ErrCandidateReadSnapshotInvalid, groupID)
		}
		seen := make(map[int64]struct{}, len(members))
		for _, member := range members {
			if _, ok := snapshot.Accounts[member.AccountID]; !ok {
				return fmt.Errorf("%w: membership references missing account %d", ErrCandidateReadSnapshotInvalid, member.AccountID)
			}
			if _, ok := seen[member.AccountID]; ok {
				return fmt.Errorf("%w: duplicate membership account %d", ErrCandidateReadSnapshotInvalid, member.AccountID)
			}
			seen[member.AccountID] = struct{}{}
		}
	}
	return nil
}

// BuildCandidateReadSnapshot projects existing account/group owners into a
// credential-free read model. The caller supplies the publication metadata;
// no model or endpoint policy is invented here.
func BuildCandidateReadSnapshot(accounts []Account, groups []Group, readRevision, sourceWatermark uint64, publishedAt time.Time) (CandidateReadSnapshot, error) {
	snapshot := CandidateReadSnapshot{
		ReadRevision: readRevision, SourceWatermark: sourceWatermark,
		PublishedAt: publishedAt, Complete: true,
		Accounts:     make(map[int64]CandidateAccountFacts, len(accounts)),
		Capabilities: make(map[string]CandidateCapabilityFacts),
		Groups:       make(map[int64]CandidateGroupFacts, len(groups)),
		Memberships:  make(map[int64][]CandidateMembership, len(groups)),
		Indexes:      CandidateIndexes{AccountsByGroup: make(map[int64][]int64), AccountsByPlatform: make(map[string][]int64)},
	}
	for _, group := range groups {
		if group.ID <= 0 {
			return CandidateReadSnapshot{}, fmt.Errorf("%w: group id %d", ErrCandidateReadSnapshotInvalid, group.ID)
		}
		if _, exists := snapshot.Groups[group.ID]; exists {
			return CandidateReadSnapshot{}, fmt.Errorf("%w: duplicate group %d", ErrCandidateReadSnapshotInvalid, group.ID)
		}
		snapshot.Groups[group.ID] = CandidateGroupFacts{
			ID: group.ID, Active: group.IsActive(), Platform: group.Platform,
			ModelAllowlist: cloneGroupModelAllowlist(group.ModelAllowlist),
			EndpointPolicy: GroupEndpointPolicy{RequirePrivacySet: group.RequirePrivacySet, RequireOAuthOnly: group.RequireOAuthOnly},
			DirectPolicy:   CandidateDirectPolicyProjection{Platform: group.Platform, DefaultMappedModel: group.DefaultMappedModel, ModelAllowlist: cloneGroupModelAllowlist(group.ModelAllowlist)},
		}
		snapshot.Memberships[group.ID] = []CandidateMembership{}
	}
	for _, account := range accounts {
		if account.ID <= 0 {
			return CandidateReadSnapshot{}, fmt.Errorf("%w: account id %d", ErrCandidateReadSnapshotInvalid, account.ID)
		}
		if _, exists := snapshot.Accounts[account.ID]; exists {
			return CandidateReadSnapshot{}, fmt.Errorf("%w: duplicate account %d", ErrCandidateReadSnapshotInvalid, account.ID)
		}
		groupIDs := append([]int64(nil), account.GroupIDs...)
		priority := make(map[int64]int, len(account.AccountGroups))
		for _, membership := range account.AccountGroups {
			groupIDs = append(groupIDs, membership.GroupID)
			priority[membership.GroupID] = membership.Priority
		}
		groupIDs = normalizePositiveIDs(groupIDs)
		facts := CandidateAccountFacts{ID: account.ID, Platform: account.Platform, ModelMapping: cloneCandidateStringMap(account.GetModelMapping()), AccountGroupIDs: groupIDs}
		snapshot.Accounts[account.ID] = facts
		snapshot.Indexes.AccountsByPlatform[account.Platform] = append(snapshot.Indexes.AccountsByPlatform[account.Platform], account.ID)
		if capability := account.ProtocolEndpointCapability; capability != nil && strings.TrimSpace(capability.CapabilityKey) != "" {
			key := capability.CapabilityKey
			if existing, ok := snapshot.Capabilities[key]; ok && existing.Revision != capability.Revision {
				return CandidateReadSnapshot{}, fmt.Errorf("%w: capability %s revision conflict", ErrCandidateReadSnapshotInvalid, key)
			}
			snapshot.Capabilities[key] = CandidateCapabilityFacts{
				CapabilityKey:   key,
				NativeProtocols: append([]protocolrouter.Protocol(nil), capability.SupportedProtocols...),
				EndpointIdentity: EndpointIdentityProjection{
					Platform: capability.Identity.Platform, EndpointProfile: capability.Identity.EndpointProfile,
					ChannelType: capability.Identity.ChannelType, UpstreamRequestProfile: capability.Identity.UpstreamRequestProfile,
				},
				EndpointPolicyDigest: endpointPolicyDigest(capability), Revision: capability.Revision,
			}
			facts.CapabilityKey = key
			snapshot.Accounts[account.ID] = facts
		}
		for _, groupID := range groupIDs {
			if _, ok := snapshot.Groups[groupID]; !ok {
				return CandidateReadSnapshot{}, fmt.Errorf("%w: account %d references missing group %d", ErrCandidateReadSnapshotInvalid, account.ID, groupID)
			}
			snapshot.Memberships[groupID] = append(snapshot.Memberships[groupID], CandidateMembership{AccountID: account.ID, Priority: priority[groupID]})
			snapshot.Indexes.AccountsByGroup[groupID] = append(snapshot.Indexes.AccountsByGroup[groupID], account.ID)
		}
	}
	for platform := range snapshot.Indexes.AccountsByPlatform {
		sort.Slice(snapshot.Indexes.AccountsByPlatform[platform], func(i, j int) bool {
			return snapshot.Indexes.AccountsByPlatform[platform][i] < snapshot.Indexes.AccountsByPlatform[platform][j]
		})
	}
	for groupID := range snapshot.Indexes.AccountsByGroup {
		sort.Slice(snapshot.Indexes.AccountsByGroup[groupID], func(i, j int) bool {
			return snapshot.Indexes.AccountsByGroup[groupID][i] < snapshot.Indexes.AccountsByGroup[groupID][j]
		})
	}
	return snapshot, ValidateCandidateReadSnapshot(snapshot)
}

func endpointPolicyDigest(capability *ProtocolEndpointCapability) string {
	if capability == nil {
		return ""
	}
	protocols := make([]string, 0, len(capability.SupportedProtocols))
	for _, protocol := range capability.SupportedProtocols {
		protocols = append(protocols, string(protocol))
	}
	sort.Strings(protocols)
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", capability.CapabilityKey, capability.Revision, strings.Join(protocols, ","))))
	return hex.EncodeToString(hash[:])
}

func scopeCandidateReadSnapshot(snapshot CandidateReadSnapshot, groupIDs []int64) (CandidateReadSnapshot, bool) {
	if err := ValidateCandidateReadSnapshot(snapshot); err != nil {
		return CandidateReadSnapshot{}, false
	}
	ids := normalizePositiveIDs(groupIDs)
	if len(ids) == 0 {
		return cloneCandidateReadSnapshot(snapshot), true
	}
	for _, groupID := range ids {
		if _, ok := snapshot.Groups[groupID]; !ok {
			return CandidateReadSnapshot{}, false
		}
		if _, ok := snapshot.Memberships[groupID]; !ok {
			return CandidateReadSnapshot{}, false
		}
	}
	result := cloneCandidateReadSnapshot(snapshot)
	result.Groups = make(map[int64]CandidateGroupFacts, len(ids))
	result.Memberships = make(map[int64][]CandidateMembership, len(ids))
	result.Accounts = make(map[int64]CandidateAccountFacts)
	result.Capabilities = make(map[string]CandidateCapabilityFacts)
	result.Indexes = CandidateIndexes{AccountsByGroup: make(map[int64][]int64), AccountsByPlatform: make(map[string][]int64)}
	for _, groupID := range ids {
		result.Groups[groupID] = snapshot.Groups[groupID]
		members := append([]CandidateMembership(nil), snapshot.Memberships[groupID]...)
		result.Memberships[groupID] = members
		for _, member := range members {
			account, ok := snapshot.Accounts[member.AccountID]
			if !ok {
				return CandidateReadSnapshot{}, false
			}
			result.Accounts[account.ID] = account
			result.Indexes.AccountsByGroup[groupID] = append(result.Indexes.AccountsByGroup[groupID], account.ID)
			result.Indexes.AccountsByPlatform[account.Platform] = append(result.Indexes.AccountsByPlatform[account.Platform], account.ID)
			if account.CapabilityKey != "" {
				capability, ok := snapshot.Capabilities[account.CapabilityKey]
				if !ok {
					return CandidateReadSnapshot{}, false
				}
				result.Capabilities[account.CapabilityKey] = capability
			}
		}
	}
	return result, true
}

func cloneCandidateReadSnapshot(snapshot CandidateReadSnapshot) CandidateReadSnapshot {
	result := snapshot
	result.Accounts = make(map[int64]CandidateAccountFacts, len(snapshot.Accounts))
	for id, account := range snapshot.Accounts {
		account.ModelMapping = cloneCandidateStringMap(account.ModelMapping)
		account.AccountGroupIDs = append([]int64(nil), account.AccountGroupIDs...)
		result.Accounts[id] = account
	}
	result.Capabilities = make(map[string]CandidateCapabilityFacts, len(snapshot.Capabilities))
	for key, capability := range snapshot.Capabilities {
		capability.NativeProtocols = append([]protocolrouter.Protocol(nil), capability.NativeProtocols...)
		result.Capabilities[key] = capability
	}
	result.Groups = make(map[int64]CandidateGroupFacts, len(snapshot.Groups))
	for id, group := range snapshot.Groups {
		group.ModelAllowlist = cloneGroupModelAllowlist(group.ModelAllowlist)
		group.DirectPolicy.ModelAllowlist = cloneGroupModelAllowlist(group.DirectPolicy.ModelAllowlist)
		result.Groups[id] = group
	}
	result.Memberships = make(map[int64][]CandidateMembership, len(snapshot.Memberships))
	for id, members := range snapshot.Memberships {
		result.Memberships[id] = append([]CandidateMembership(nil), members...)
	}
	result.Indexes = CandidateIndexes{AccountsByGroup: make(map[int64][]int64), AccountsByPlatform: make(map[string][]int64)}
	for id, accounts := range snapshot.Indexes.AccountsByGroup {
		result.Indexes.AccountsByGroup[id] = append([]int64(nil), accounts...)
	}
	for platform, accounts := range snapshot.Indexes.AccountsByPlatform {
		result.Indexes.AccountsByPlatform[platform] = append([]int64(nil), accounts...)
	}
	return result
}

func cloneGroupModelAllowlist(value GroupModelAllowlist) GroupModelAllowlist {
	value.Models = append([]string(nil), value.Models...)
	return value
}

func cloneCandidateStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func normalizePositiveIDs(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

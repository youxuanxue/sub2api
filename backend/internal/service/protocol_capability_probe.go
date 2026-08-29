package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

type ProtocolProbeVerdict string

type ProtocolProbeRunOutcome string

const (
	ProtocolProbePositive         ProtocolProbeVerdict = "positive"
	ProtocolProbeEndpointNegative ProtocolProbeVerdict = "endpoint_negative"
	ProtocolProbeInconclusive     ProtocolProbeVerdict = "inconclusive"
	ProtocolProbeModelSpecific    ProtocolProbeVerdict = "model_specific"
)

const (
	ProtocolProbeRunUpdated       ProtocolProbeRunOutcome = "updated"
	ProtocolProbeRunUnchanged     ProtocolProbeRunOutcome = "unchanged"
	ProtocolProbeRunInconclusive  ProtocolProbeRunOutcome = "inconclusive"
	ProtocolProbeRunNotApplicable ProtocolProbeRunOutcome = "not_applicable"
)

type ProtocolProbeRunResult struct {
	Outcome              ProtocolProbeRunOutcome
	Reason               string
	Capability           *ProtocolEndpointCapability
	AffectedAccountCount int
}

type protocolProbeCoordinator struct {
	group singleflight.Group
}

func (c *protocolProbeCoordinator) Do(
	capabilityKey string,
	job func() (ProtocolProbeRunResult, error),
) (ProtocolProbeRunResult, error) {
	if job == nil {
		return ProtocolProbeRunResult{}, nil
	}
	value, err, _ := c.group.Do(capabilityKey, func() (any, error) {
		return job()
	})
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	result, _ := value.(ProtocolProbeRunResult)
	return result, nil
}

type protocolProbeObservation struct {
	protocol protocolrouter.Protocol
	verdict  ProtocolProbeVerdict
}

func protocolProbeRelayCapacityVerdict(
	account *Account,
	statusCode int,
	body []byte,
) (ProtocolProbeVerdict, bool) {
	if tkIsAntigravityRelayCapacityResponse(account, statusCode, body) {
		return ProtocolProbePositive, true
	}
	return "", false
}

type protocolProbeGenerationResolution struct {
	SupportedProtocols []protocolrouter.Protocol
	Evidence           map[protocolrouter.Protocol]any
	IdentityConflict   bool
	AllConclusive      bool
}

func (s *AccountTestService) ProbeAccountProtocolCapabilities(ctx context.Context, accountID int64) {
	_, _ = s.ProbeAccountProtocolCapabilitiesNow(ctx, accountID)
}

func (s *AccountTestService) ProbeAccountProtocolCapabilitiesForPreparation(ctx context.Context, accountID int64) {
	_, _ = s.probeAccountProtocolCapabilitiesNow(ctx, accountID, false)
}

func (s *AccountTestService) ProbeAccountProtocolCapabilitiesNow(ctx context.Context, accountID int64) (ProtocolProbeRunResult, error) {
	return s.probeAccountProtocolCapabilitiesNow(ctx, accountID, true)
}

func (s *AccountTestService) probeAccountProtocolCapabilitiesNow(ctx context.Context, accountID int64, publish bool) (ProtocolProbeRunResult, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	identity, governed, err := BuildProtocolEndpointIdentity(account)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	if !governed {
		return ProtocolProbeRunResult{Outcome: ProtocolProbeRunNotApplicable, Reason: "account_not_governed"}, nil
	}
	capabilityRepo := s.protocolCapabilityRepo
	if capabilityRepo == nil {
		capabilityRepo, _ = s.accountRepo.(ProtocolEndpointCapabilityRepository)
	}
	if capabilityRepo == nil {
		return ProtocolProbeRunResult{}, errors.New("protocol endpoint capability repository is required")
	}
	official := officialSupportedProtocols(account)
	capability, err := capabilityRepo.EnsureAccountLink(ctx, account, identity, official, len(official) > 0)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	candidates := ProtocolProbeCandidates(account)
	if len(candidates) == 0 {
		return ProtocolProbeRunResult{
			Outcome:    ProtocolProbeRunNotApplicable,
			Reason:     "no_protocol_probe_candidates",
			Capability: capability,
		}, nil
	}
	return s.protocolProbeCoordinator.Do(capability.CapabilityKey, func() (ProtocolProbeRunResult, error) {
		return s.runEndpointProtocolProbe(ctx, capabilityRepo, capability, candidates, publish)
	})
}

const (
	protocolProbeLeaseTTL     = 2 * time.Minute
	protocolProbeMaxWitnesses = 3
)

func (s *AccountTestService) runEndpointProtocolProbe(
	ctx context.Context,
	repo ProtocolEndpointCapabilityRepository,
	capability *ProtocolEndpointCapability,
	candidates []protocolrouter.Protocol,
	publish bool,
) (ProtocolProbeRunResult, error) {
	beforeProtocols := append([]protocolrouter.Protocol(nil), capability.SupportedProtocols...)
	lease, acquired, err := repo.AcquireProbeLease(ctx, capability.CapabilityKey, uuid.NewString(), time.Now().UTC(), protocolProbeLeaseTTL)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	if !acquired {
		return ProtocolProbeRunResult{Outcome: ProtocolProbeRunInconclusive, Reason: "probe_lease_busy", Capability: capability}, nil
	}
	linkedIDs, err := repo.ListLinkedAccountIDs(ctx, capability.CapabilityKey)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	linked, err := s.accountRepo.GetByIDs(ctx, linkedIDs)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	witnesses := selectProtocolProbeWitnesses(linked)
	if len(witnesses) == 0 {
		updated, affected, commitErr := commitProtocolProbeResult(ctx, repo, lease, ProtocolCapabilityMutation{
			SupportedProtocols: capability.SupportedProtocols,
			ProbeEvidence:      capability.ProbeEvidence,
			IdentityConflict:   capability.IdentityConflict || capability.ProbeEvidence.IdentityConflict,
			LastProbedAt:       time.Now().UTC(),
		}, publish)
		if commitErr != nil {
			return ProtocolProbeRunResult{}, commitErr
		}
		return ProtocolProbeRunResult{Outcome: ProtocolProbeRunInconclusive, Reason: "no_usable_witness", Capability: updated, AffectedAccountCount: affected}, nil
	}

	type probeResult struct {
		protocol protocolrouter.Protocol
		verdicts []ProtocolProbeVerdict
	}
	results := make([]probeResult, len(candidates))
	var probes sync.WaitGroup
	probes.Add(len(candidates))
	for i, candidate := range candidates {
		go func(index int, protocol protocolrouter.Protocol) {
			defer probes.Done()
			results[index].protocol = protocol
			limit := min(protocolProbeMaxWitnesses, len(witnesses))
			results[index].verdicts = probeProtocolWitnesses(witnesses[:limit], func(witness *Account) (protocolProbeObservation, bool) {
				return s.probeProtocolCapability(ctx, witness, protocol)
			})
		}(i, candidate)
	}
	probes.Wait()

	observations := make(map[protocolrouter.Protocol][]ProtocolProbeVerdict, len(results))
	for _, result := range results {
		observations[result.protocol] = append([]ProtocolProbeVerdict(nil), result.verdicts...)
	}
	resolution, err := resolveProtocolProbeGeneration(
		capability.SupportedProtocols,
		capability.IdentityConflict || capability.ProbeEvidence.IdentityConflict,
		capability.ProbeEvidence.Verdicts,
		candidates,
		observations,
	)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	evidence := capability.ProbeEvidence
	verdictEvidence := make(map[string]any, len(evidence.Verdicts)+len(resolution.Evidence))
	for protocol, verdict := range evidence.Verdicts {
		verdictEvidence[protocol] = verdict
	}
	for protocol, verdict := range resolution.Evidence {
		verdictEvidence[string(protocol)] = verdict
	}
	evidence.Verdicts = verdictEvidence
	updated, affected, err := commitProtocolProbeResult(ctx, repo, lease, ProtocolCapabilityMutation{
		SupportedProtocols:    resolution.SupportedProtocols,
		ProbeEvidence:         evidence,
		InitialProbeCompleted: resolution.AllConclusive,
		IdentityConflict:      resolution.IdentityConflict,
		LastProbedAt:          time.Now().UTC(),
	}, publish)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	outcome := ProtocolProbeRunUnchanged
	reason := "no_conclusive_capability_change"
	conflictChanged := capability.IdentityConflict != updated.IdentityConflict
	if !protocolListsEqual(beforeProtocols, updated.SupportedProtocols) || conflictChanged {
		outcome = ProtocolProbeRunUpdated
		if updated.IdentityConflict {
			reason = "conflicting_endpoint_evidence"
		} else {
			reason = "positive_or_endpoint_negative_evidence"
		}
	} else if !resolution.AllConclusive {
		outcome = ProtocolProbeRunInconclusive
		reason = "inconclusive_evidence"
	}
	return ProtocolProbeRunResult{Outcome: outcome, Reason: reason, Capability: updated, AffectedAccountCount: affected}, nil
}

func commitProtocolProbeResult(
	ctx context.Context,
	repo ProtocolEndpointCapabilityRepository,
	lease ProtocolProbeLease,
	mutation ProtocolCapabilityMutation,
	publish bool,
) (*ProtocolEndpointCapability, int, error) {
	if publish {
		return repo.CommitProbeResult(ctx, lease, mutation)
	}
	return repo.CommitPreparedProbeResult(ctx, lease, mutation)
}

func resolveProtocolProbeGeneration(
	prior []protocolrouter.Protocol,
	priorIdentityConflict bool,
	priorEvidence map[string]any,
	candidates []protocolrouter.Protocol,
	observations map[protocolrouter.Protocol][]ProtocolProbeVerdict,
) (protocolProbeGenerationResolution, error) {
	normalized, err := NormalizeSupportedProtocols(prior)
	if err != nil {
		return protocolProbeGenerationResolution{}, err
	}
	set := make(map[protocolrouter.Protocol]struct{}, len(normalized))
	for _, protocol := range normalized {
		set[protocol] = struct{}{}
	}
	resolution := protocolProbeGenerationResolution{
		Evidence:         make(map[protocolrouter.Protocol]any, len(candidates)),
		IdentityConflict: priorIdentityConflict,
		AllConclusive:    len(candidates) > 0,
	}
	conflictObserved := false
	for _, protocol := range candidates {
		if !protocol.Valid() {
			return protocolProbeGenerationResolution{}, fmt.Errorf("invalid probed protocol %q", protocol)
		}
		verdicts := observations[protocol]
		sawPositive := slices.Contains(verdicts, ProtocolProbePositive)
		sawNegative := slices.Contains(verdicts, ProtocolProbeEndpointNegative)
		priorVerdict := conclusiveProtocolProbeVerdict(priorEvidence[string(protocol)])
		switch {
		case sawPositive && sawNegative,
			sawPositive && priorVerdict == ProtocolProbeEndpointNegative,
			sawNegative && priorVerdict == ProtocolProbePositive:
			delete(set, protocol)
			resolution.Evidence[protocol] = "conflict"
			resolution.AllConclusive = false
			conflictObserved = true
		case sawPositive:
			set[protocol] = struct{}{}
			resolution.Evidence[protocol] = string(ProtocolProbePositive)
		case sawNegative:
			delete(set, protocol)
			resolution.Evidence[protocol] = string(ProtocolProbeEndpointNegative)
		case slices.Contains(verdicts, ProtocolProbeModelSpecific):
			resolution.Evidence[protocol] = persistedProtocolProbeVerdict(priorVerdict, ProtocolProbeModelSpecific)
			resolution.AllConclusive = false
		case slices.Contains(verdicts, ProtocolProbeInconclusive):
			resolution.Evidence[protocol] = persistedProtocolProbeVerdict(priorVerdict, ProtocolProbeInconclusive)
			resolution.AllConclusive = false
		default:
			resolution.AllConclusive = false
		}
	}
	if conflictObserved {
		resolution.IdentityConflict = true
	} else if resolution.AllConclusive {
		resolution.IdentityConflict = false
	}
	resolution.SupportedProtocols = make([]protocolrouter.Protocol, 0, len(set))
	for _, protocol := range protocolrouter.AllProtocols() {
		if _, ok := set[protocol]; ok {
			resolution.SupportedProtocols = append(resolution.SupportedProtocols, protocol)
		}
	}
	return resolution, nil
}

func persistedProtocolProbeVerdict(prior, observed ProtocolProbeVerdict) string {
	if (observed == ProtocolProbeInconclusive || observed == ProtocolProbeModelSpecific) &&
		(prior == ProtocolProbePositive || prior == ProtocolProbeEndpointNegative) {
		return string(prior)
	}
	return string(observed)
}

func conclusiveProtocolProbeVerdict(raw any) ProtocolProbeVerdict {
	value, _ := raw.(string)
	verdict := ProtocolProbeVerdict(value)
	if verdict == ProtocolProbePositive || verdict == ProtocolProbeEndpointNegative {
		return verdict
	}
	return ""
}

func probeProtocolWitnesses(
	witnesses []*Account,
	probe func(*Account) (protocolProbeObservation, bool),
) []ProtocolProbeVerdict {
	if probe == nil {
		return nil
	}
	verdicts := make([]ProtocolProbeVerdict, 0, len(witnesses))
	for _, witness := range witnesses {
		observation, observed := probe(witness)
		if !observed {
			continue
		}
		verdicts = append(verdicts, observation.verdict)
		if observation.verdict == ProtocolProbePositive ||
			observation.verdict == ProtocolProbeEndpointNegative ||
			observation.verdict == ProtocolProbeModelSpecific {
			break
		}
	}
	return verdicts
}

func selectProtocolProbeWitnesses(accounts []*Account) []*Account {
	witnesses := make([]*Account, 0, len(accounts))
	now := time.Now().UTC()
	for _, account := range accounts {
		if account == nil || !account.IsActive() || !account.Schedulable || strings.TrimSpace(account.ErrorMessage) != "" || !protocolProbeAuthorizationUsable(account, now) {
			continue
		}
		witnesses = append(witnesses, account)
	}
	sort.Slice(witnesses, func(i, j int) bool {
		if witnesses[i].Priority != witnesses[j].Priority {
			return witnesses[i].Priority < witnesses[j].Priority
		}
		return witnesses[i].ID < witnesses[j].ID
	})
	return witnesses
}

func protocolProbeAuthorizationUsable(account *Account, now time.Time) bool {
	if account == nil || account.ParentAccountID != nil {
		return false
	}
	if account.ExpiresAt != nil && !account.ExpiresAt.After(now) {
		return false
	}
	if expiresAt := account.GetCredentialAsTime("expires_at"); expiresAt != nil && !expiresAt.After(now) {
		return false
	}
	return ProtocolAuthorizationPresent(account)
}

func protocolListsEqual(left, right []protocolrouter.Protocol) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// ProbeAccountProtocolCapabilitiesBatch probes accounts with the same bounded
// concurrency used during startup preparation.
func (s *AccountTestService) ProbeAccountProtocolCapabilitiesBatch(ctx context.Context, accountIDs []int64) {
	probeProtocolRoutingAccounts(ctx, s, accountIDs)
}

func (s *AccountTestService) probeProtocolCapability(
	ctx context.Context,
	account *Account,
	protocol protocolrouter.Protocol,
) (protocolProbeObservation, bool) {
	switch protocol {
	case protocolrouter.ProtocolMessages:
		return s.probeOpenAIAPIKeyNativeMessagesSupport(ctx, account)
	case protocolrouter.ProtocolChatCompletions:
		return s.probeOpenAIAPIKeyChatCompletionsSupport(ctx, account)
	case protocolrouter.ProtocolResponses:
		return s.probeOpenAIAPIKeyResponsesSupport(ctx, account)
	case protocolrouter.ProtocolGeminiGenerateContent:
		return s.probeGeminiGenerateContentSupport(ctx, account)
	default:
		return protocolProbeObservation{}, false
	}
}

func ApplyProtocolProbeVerdicts(
	prior []protocolrouter.Protocol,
	verdicts map[protocolrouter.Protocol]ProtocolProbeVerdict,
) ([]protocolrouter.Protocol, error) {
	normalized, err := NormalizeSupportedProtocols(prior)
	if err != nil {
		return nil, err
	}
	set := make(map[protocolrouter.Protocol]struct{}, len(normalized))
	for _, protocol := range normalized {
		set[protocol] = struct{}{}
	}
	for protocol, verdict := range verdicts {
		if !protocol.Valid() {
			return nil, fmt.Errorf("invalid probed protocol %q", protocol)
		}
		switch verdict {
		case ProtocolProbePositive:
			set[protocol] = struct{}{}
		case ProtocolProbeEndpointNegative:
			delete(set, protocol)
		case ProtocolProbeInconclusive, ProtocolProbeModelSpecific:
			// Preserve the prior endpoint fact.
		default:
			return nil, fmt.Errorf("invalid protocol probe verdict %q", verdict)
		}
	}
	result := make([]protocolrouter.Protocol, 0, len(set))
	for _, protocol := range protocolrouter.AllProtocols() {
		if _, ok := set[protocol]; ok {
			result = append(result, protocol)
		}
	}
	return result, nil
}

func protocolProbeBaseURL(account *Account, protocol protocolrouter.Protocol) string {
	baseURL, protocolBaseURLs, _ := protocolAccountEndpoints(account)
	if protocolBaseURL := protocolBaseURLs[protocol]; protocolBaseURL != "" {
		return protocolBaseURL
	}
	return baseURL
}

// protocolAuthorizationToken is the single credential-binding owner for
// protocol routing and capability probes. It returns the bearer/API-key
// material without inferring endpoint capability.
func protocolAuthorizationToken(account *Account) string {
	if account == nil {
		return ""
	}
	if account.IsGrokOAuth() {
		return account.GetGrokAccessToken()
	}
	if account.IsOpenAIOAuthLike() {
		return account.GetOpenAIAccessToken()
	}
	if account.Platform == PlatformAnthropic && account.IsAnthropicOAuthOrSetupToken() {
		return account.GetCredential("access_token")
	}
	if token := account.GetOpenAIProtocolAPIKey(); token != "" {
		return token
	}
	return account.GetCredential("api_key")
}

func applyProtocolProbeRequestIdentity(
	req *http.Request,
	account *Account,
	protocol protocolrouter.Protocol,
) *http.Request {
	if req == nil {
		return nil
	}
	profile := HTTPUpstreamProfileOpenAI
	if account != nil && account.IsGrok() {
		profile = HTTPUpstreamProfileGrok
	} else if protocol == protocolrouter.ProtocolResponses {
		applyOpenAICodexProbeHeaders(req.Header)
	}
	reqCtx := WithHTTPUpstreamProfile(req.Context(), profile)
	req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(reqCtx))
	if account != nil && account.IsGrokOAuth() {
		applyGrokCLIHeaders(req.Header)
	}
	if account != nil {
		account.ApplyHeaderOverrides(req.Header)
	}
	return req
}

func ProtocolProbeCandidates(account *Account) []protocolrouter.Protocol {
	if !protocolRoutingGovernsAccount(account) || len(officialSupportedProtocols(account)) > 0 {
		return nil
	}
	if protocolGeminiEndpointProfile(account).Valid() {
		return []protocolrouter.Protocol{protocolrouter.ProtocolGeminiGenerateContent}
	}
	switch {
	case account.Type == AccountTypeAPIKey, account.Type == AccountTypeUpstream:
	case account.IsGrokOAuth():
	case account.Platform == PlatformAnthropic &&
		account.IsAnthropicOAuthOrSetupToken() &&
		account.IsCustomBaseURLEnabled():
	default:
		return nil
	}
	candidates := make([]protocolrouter.Protocol, 0, len(protocolrouter.AllProtocols()))
	for _, protocol := range protocolrouter.AllProtocols() {
		if protocol == protocolrouter.ProtocolGeminiGenerateContent {
			continue
		}
		if protocol == protocolrouter.ProtocolMessages && tkIsOpenAIEdgeMirrorStub(account) {
			continue
		}
		if strings.TrimSpace(protocolProbeBaseURL(account, protocol)) != "" {
			candidates = append(candidates, protocol)
		}
	}
	return candidates
}

func protocolProbeSupports(account *Account, protocol protocolrouter.Protocol) bool {
	for _, candidate := range ProtocolProbeCandidates(account) {
		if candidate == protocol {
			return true
		}
	}
	return false
}

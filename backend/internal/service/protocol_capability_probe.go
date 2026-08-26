package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
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
	ProtocolProbeRunNotApplicable ProtocolProbeRunOutcome = "not_applicable"
)

type ProtocolProbeRunResult struct {
	Outcome ProtocolProbeRunOutcome
	Reason  string
}

var ErrProtocolProbeStaleRevision = errors.New("protocol probe account revision is stale")

var (
	ErrProtocolProbeAtomicWriterMissing = errors.New("protocol probe repository does not support atomic revision writes")
	ErrProtocolProbeConcurrentMutation  = errors.New("protocol probe account changed during persistence")
)

const protocolProbeCASRetries = 8

type protocolProbeRepository interface {
	GetByID(ctx context.Context, id int64) (*Account, error)
}

type protocolProbeAtomicWriter interface {
	UpdateExtraIfUpdatedAt(
		ctx context.Context,
		id int64,
		expectedUpdatedAt time.Time,
		updates map[string]any,
	) (bool, error)
}

type protocolProbeCoordinator struct {
	group singleflight.Group
}

func (c *protocolProbeCoordinator) Do(
	accountID int64,
	configurationRevision string,
	candidates []protocolrouter.Protocol,
	job func() error,
) error {
	if job == nil {
		return nil
	}
	parts := make([]string, len(candidates))
	for i, protocol := range candidates {
		parts[i] = string(protocol)
	}
	key := fmt.Sprintf("%d\x00%s\x00%s", accountID, configurationRevision, strings.Join(parts, ","))
	_, err, _ := c.group.Do(key, func() (any, error) {
		return nil, job()
	})
	return err
}

type protocolProbeObservation struct {
	protocol      protocolrouter.Protocol
	verdict       ProtocolProbeVerdict
	legacyUpdates map[string]any
}

func (s *AccountTestService) ProbeAccountProtocolCapabilities(ctx context.Context, accountID int64) {
	_, _ = s.ProbeAccountProtocolCapabilitiesNow(ctx, accountID)
}

func (s *AccountTestService) ProbeAccountProtocolCapabilitiesNow(ctx context.Context, accountID int64) (ProtocolProbeRunResult, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	candidates := ProtocolProbeCandidates(account)
	if len(candidates) == 0 {
		return ProtocolProbeRunResult{
			Outcome: ProtocolProbeRunNotApplicable,
			Reason:  "no_protocol_probe_candidates",
		}, nil
	}
	before := account.SupportedProtocols()
	revision, err := protocolProbeConfigurationRevision(account)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	if err := s.protocolProbeCoordinator.Do(accountID, revision, candidates, func() error {
		type probeResult struct {
			observation protocolProbeObservation
			observed    bool
		}
		results := make([]probeResult, len(candidates))
		var probes sync.WaitGroup
		probes.Add(len(candidates))
		for i, protocol := range candidates {
			go func(index int, candidate protocolrouter.Protocol) {
				defer probes.Done()
				accountSnapshot := *account
				results[index].observation, results[index].observed = s.probeProtocolCapability(ctx, &accountSnapshot, revision, candidate)
			}(i, protocol)
		}
		probes.Wait()

		verdicts := make(map[protocolrouter.Protocol]ProtocolProbeVerdict, len(candidates))
		legacyUpdates := make(map[string]any)
		for _, result := range results {
			if !result.observed {
				continue
			}
			observation := result.observation
			verdicts[observation.protocol] = observation.verdict
			for key, value := range observation.legacyUpdates {
				legacyUpdates[key] = value
			}
		}
		if len(verdicts) == 0 {
			return nil
		}
		return PersistProtocolProbeVerdicts(ctx, s.accountRepo, accountID, revision, verdicts, legacyUpdates)
	}); err != nil {
		return ProtocolProbeRunResult{}, err
	}
	refreshed, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return ProtocolProbeRunResult{}, err
	}
	after := refreshed.SupportedProtocols()
	if protocolListsEqual(before, after) {
		return ProtocolProbeRunResult{
			Outcome: ProtocolProbeRunUnchanged,
			Reason:  "no_conclusive_capability_change",
		}, nil
	}
	return ProtocolProbeRunResult{Outcome: ProtocolProbeRunUpdated}, nil
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
	revision string,
	protocol protocolrouter.Protocol,
) (protocolProbeObservation, bool) {
	switch protocol {
	case protocolrouter.ProtocolMessages:
		return s.probeOpenAIAPIKeyNativeMessagesSupport(ctx, account, revision)
	case protocolrouter.ProtocolChatCompletions:
		return s.probeOpenAIAPIKeyChatCompletionsSupport(ctx, account, revision)
	case protocolrouter.ProtocolResponses:
		return s.probeOpenAIAPIKeyResponsesSupport(ctx, account, revision)
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

func BuildProtocolProbeUpdate(
	account *Account,
	expectedRevision string,
	verdicts map[protocolrouter.Protocol]ProtocolProbeVerdict,
) (map[string]any, error) {
	if account == nil {
		return nil, errors.New("account is required")
	}
	currentRevision, err := protocolProbeConfigurationRevision(account)
	if err != nil {
		return nil, err
	}
	if expectedRevision == "" || currentRevision != expectedRevision {
		return nil, ErrProtocolProbeStaleRevision
	}
	protocols, err := ApplyProtocolProbeVerdicts(account.SupportedProtocols(), verdicts)
	if err != nil {
		return nil, err
	}
	return BuildSupportedProtocolsUpdate(protocols)
}

func protocolProbeConfigurationRevision(account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is required")
	}
	configuration := *account
	configuration.UpdatedAt = time.Time{}
	configuration.Extra = make(map[string]any, len(account.Extra))
	for key, value := range account.Extra {
		switch key {
		case SupportedProtocolsExtraKey,
			openai_compat.ExtraKeyResponsesSupported,
			openai_compat.ExtraKeyNativeMessagesSupported:
			continue
		default:
			configuration.Extra[key] = value
		}
	}
	return protocolAccountRevision(&configuration)
}

func protocolProbeBaseURL(account *Account, protocol protocolrouter.Protocol) string {
	baseURL, protocolBaseURLs, _ := protocolAccountEndpoints(account)
	if protocolBaseURL := protocolBaseURLs[protocol]; protocolBaseURL != "" {
		return protocolBaseURL
	}
	return baseURL
}

// protocolProbeAuthToken is the single credential-binding owner for protocol
// capability probes. It returns the bearer/API-key material used by the
// protocol-specific request builder without inferring endpoint capability.
func protocolProbeAuthToken(account *Account) string {
	if account == nil {
		return ""
	}
	if account.IsGrokOAuth() {
		return account.GetGrokAccessToken()
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

func PersistProtocolProbeVerdicts(
	ctx context.Context,
	repo protocolProbeRepository,
	accountID int64,
	expectedConfigurationRevision string,
	verdicts map[protocolrouter.Protocol]ProtocolProbeVerdict,
	legacyUpdates map[string]any,
) error {
	if repo == nil {
		return errors.New("protocol probe repository is required")
	}
	writer, ok := repo.(protocolProbeAtomicWriter)
	if !ok {
		return ErrProtocolProbeAtomicWriterMissing
	}
	for range protocolProbeCASRetries {
		if err := ctx.Err(); err != nil {
			return err
		}
		account, err := repo.GetByID(ctx, accountID)
		if err != nil {
			return err
		}
		updates, err := BuildProtocolProbeUpdate(account, expectedConfigurationRevision, verdicts)
		if err != nil {
			return err
		}
		for key, value := range legacyUpdates {
			updates[key] = value
		}
		updated, err := writer.UpdateExtraIfUpdatedAt(ctx, accountID, account.UpdatedAt, updates)
		if err != nil {
			return err
		}
		if updated {
			return nil
		}
	}
	return ErrProtocolProbeConcurrentMutation
}

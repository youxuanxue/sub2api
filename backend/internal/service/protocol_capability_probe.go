package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"golang.org/x/sync/singleflight"
)

type ProtocolProbeVerdict string

const (
	ProtocolProbePositive         ProtocolProbeVerdict = "positive"
	ProtocolProbeEndpointNegative ProtocolProbeVerdict = "endpoint_negative"
	ProtocolProbeInconclusive     ProtocolProbeVerdict = "inconclusive"
	ProtocolProbeModelSpecific    ProtocolProbeVerdict = "model_specific"
)

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
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return
	}
	candidates := ProtocolProbeCandidates(account)
	if len(candidates) == 0 {
		return
	}
	revision, err := protocolProbeConfigurationRevision(account)
	if err != nil {
		return
	}
	if err := s.protocolProbeCoordinator.Do(accountID, revision, candidates, func() error {
		verdicts := make(map[protocolrouter.Protocol]ProtocolProbeVerdict, len(candidates))
		legacyUpdates := make(map[string]any)
		for _, protocol := range candidates {
			observation, observed := s.probeProtocolCapability(ctx, account, revision, protocol)
			if !observed {
				continue
			}
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
		return
	}
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

func ProtocolProbeCandidates(account *Account) []protocolrouter.Protocol {
	if !protocolRoutingGovernsAccount(account) || len(officialSupportedProtocols(account)) > 0 {
		return nil
	}
	switch account.Type {
	case AccountTypeAPIKey, AccountTypeUpstream:
	default:
		return nil
	}
	candidates := make([]protocolrouter.Protocol, 0, len(protocolrouter.AllProtocols()))
	for _, protocol := range protocolrouter.AllProtocols() {
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

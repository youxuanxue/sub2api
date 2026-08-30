package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const protocolRoutingStartupProbeConcurrency = 4

type protocolRoutingCapabilityProber interface {
	ProbeAccountProtocolCapabilitiesForPreparation(ctx context.Context, accountID int64)
}

type ProtocolRoutingRemediationReason string

const (
	ProtocolRoutingRemediationProbeRequired ProtocolRoutingRemediationReason = "probe_required"
	ProtocolRoutingRemediationNoLegalRoute  ProtocolRoutingRemediationReason = "no_legal_route"
)

type ProtocolRoutingRemediation struct {
	AccountID int64
	Name      string
	Reason    ProtocolRoutingRemediationReason
	Models    []string
}

type ProtocolRoutingMigrationReport struct {
	ActiveGoverned int
	SeededOfficial int
	ProbeAttempts  int
	ProbeResolved  int
	// CutoverReady is true only when the router exists and no remediations remain.
	CutoverReady bool
	Remediation  []ProtocolRoutingRemediation
}

type protocolRoutingMigrationRepository interface {
	ListActive(ctx context.Context) ([]Account, error)
	ProtocolEndpointCapabilityRepository
	PublishProtocolRoutingProjections(ctx context.Context, validate func(context.Context) error) (int, error)
}

var errProtocolRoutingFinalReadinessNotReady = errors.New("protocol routing final readiness is not ready")

func MigrateProtocolRoutingSSOT(
	ctx context.Context,
	repo protocolRoutingMigrationRepository,
	router *protocolrouter.Router,
) (ProtocolRoutingMigrationReport, error) {
	return evaluateProtocolRoutingSSOT(ctx, repo, router, true)
}

func validateProtocolRoutingSSOTReadiness(
	ctx context.Context,
	repo protocolRoutingMigrationRepository,
	router *protocolrouter.Router,
) (ProtocolRoutingMigrationReport, error) {
	return evaluateProtocolRoutingSSOT(ctx, repo, router, false)
}

func evaluateProtocolRoutingSSOT(
	ctx context.Context,
	repo protocolRoutingMigrationRepository,
	router *protocolrouter.Router,
	prepare bool,
) (ProtocolRoutingMigrationReport, error) {
	report := ProtocolRoutingMigrationReport{CutoverReady: router != nil}
	accounts, err := repo.ListActive(ctx)
	if err != nil {
		return report, err
	}
	for i := range accounts {
		account := &accounts[i]
		if !protocolRoutingGovernsAccount(account) {
			continue
		}
		report.ActiveGoverned++
		identity, governed, err := BuildProtocolEndpointIdentity(account)
		if err != nil {
			if account.Schedulable {
				report.CutoverReady = false
				report.Remediation = append(report.Remediation, ProtocolRoutingRemediation{AccountID: account.ID, Name: account.Name, Reason: ProtocolRoutingRemediationNoLegalRoute})
			}
			continue
		}
		if !governed {
			continue
		}
		capability := account.ProtocolEndpointCapability
		if prepare {
			official := officialSupportedProtocols(account)
			wasOfficialSeed := false
			if existing, existingErr := repo.GetByKey(ctx, identity.Key()); existingErr == nil && existing != nil {
				wasOfficialSeed = existing.ProbeEvidence.OfficialSeed
			}
			capability, err = repo.EnsureAccountLink(ctx, account, identity, LegacySupportedProtocolsProjection(account), len(official) > 0)
			if err != nil {
				return report, err
			}
			if len(official) > 0 && capability.ProbeEvidence.OfficialSeed && !wasOfficialSeed {
				report.SeededOfficial++
			}
		} else if capability == nil || account.ProtocolEndpointCapabilityID == nil ||
			capability.ID != *account.ProtocolEndpointCapabilityID || capability.CapabilityKey != identity.Key() {
			if account.Schedulable {
				report.CutoverReady = false
				report.Remediation = append(report.Remediation, ProtocolRoutingRemediation{
					AccountID: account.ID,
					Name:      account.Name,
					Reason:    ProtocolRoutingRemediationProbeRequired,
				})
			}
			continue
		}
		if !account.Schedulable {
			continue
		}
		if !ProtocolAuthorizationPresent(account) {
			report.CutoverReady = false
			report.Remediation = append(report.Remediation, ProtocolRoutingRemediation{AccountID: account.ID, Name: account.Name, Reason: ProtocolRoutingRemediationProbeRequired})
			continue
		}
		if capability.IdentityConflict || capability.ProbeEvidence.IdentityConflict ||
			!protocolCapabilityHasVerifiedRoutingEvidence(capability) {
			report.CutoverReady = false
			report.Remediation = append(report.Remediation, ProtocolRoutingRemediation{
				AccountID: account.ID,
				Name:      account.Name,
				Reason:    ProtocolRoutingRemediationProbeRequired,
			})
			continue
		}
		models := protocolRoutingMigrationModels(account)
		if !accountHasLegalProtocolRoute(router, account, models) {
			report.CutoverReady = false
			report.Remediation = append(report.Remediation, ProtocolRoutingRemediation{
				AccountID: account.ID,
				Name:      account.Name,
				Reason:    ProtocolRoutingRemediationNoLegalRoute,
				Models:    models,
			})
			continue
		}
		if !capability.ProbeEvidence.InitialProbeCompleted && !capability.ProbeEvidence.OfficialSeed {
			report.CutoverReady = false
			report.Remediation = append(report.Remediation, ProtocolRoutingRemediation{
				AccountID: account.ID,
				Name:      account.Name,
				Reason:    ProtocolRoutingRemediationProbeRequired,
			})
		}
	}
	sort.Slice(report.Remediation, func(i, j int) bool {
		return report.Remediation[i].AccountID < report.Remediation[j].AccountID
	})
	return report, nil
}

func prepareProtocolRoutingSSOT(
	ctx context.Context,
	repo protocolRoutingMigrationRepository,
	router *protocolrouter.Router,
	prober protocolRoutingCapabilityProber,
) (ProtocolRoutingSSOTReady, error) {
	initial, err := MigrateProtocolRoutingSSOT(ctx, repo, router)
	if err != nil {
		return ProtocolRoutingSSOTReady{Report: initial}, err
	}
	prepared := initial
	if !initial.CutoverReady && prober != nil && len(initial.Remediation) > 0 {
		accountIDs := make([]int64, 0, len(initial.Remediation))
		seenKeys := make(map[string]struct{}, len(initial.Remediation))
		for _, remediation := range initial.Remediation {
			if remediation.AccountID <= 0 {
				continue
			}
			capability, capabilityErr := repo.GetByAccountID(ctx, remediation.AccountID)
			if capabilityErr != nil || capability == nil || capability.CapabilityKey == "" {
				continue
			}
			if _, exists := seenKeys[capability.CapabilityKey]; exists {
				continue
			}
			seenKeys[capability.CapabilityKey] = struct{}{}
			accountIDs = append(accountIDs, remediation.AccountID)
		}
		probeProtocolRoutingAccounts(ctx, prober, accountIDs)

		prepared, err = MigrateProtocolRoutingSSOT(ctx, repo, router)
		prepared.ProbeAttempts = len(accountIDs)
		prepared.ProbeResolved = protocolRoutingResolvedProbeCount(accountIDs, prepared.Remediation)
		prepared.SeededOfficial += initial.SeededOfficial
		if err != nil {
			return ProtocolRoutingSSOTReady{Report: prepared}, err
		}
	}
	if !prepared.CutoverReady {
		return newProtocolRoutingSSOTReady(prepared, router), nil
	}
	final := prepared
	_, err = repo.PublishProtocolRoutingProjections(ctx, func(txCtx context.Context) error {
		var validationErr error
		final, validationErr = validateProtocolRoutingSSOTReadiness(txCtx, repo, router)
		final.SeededOfficial += prepared.SeededOfficial
		final.ProbeAttempts = prepared.ProbeAttempts
		final.ProbeResolved = prepared.ProbeResolved
		if validationErr != nil {
			return validationErr
		}
		if !final.CutoverReady {
			return errProtocolRoutingFinalReadinessNotReady
		}
		return nil
	})
	if errors.Is(err, errProtocolRoutingFinalReadinessNotReady) {
		final.CutoverReady = false
		return newProtocolRoutingSSOTReady(final, router), nil
	}
	if err != nil {
		final.CutoverReady = false
		return ProtocolRoutingSSOTReady{Report: final}, err
	}
	return newProtocolRoutingSSOTReady(final, router), nil
}

func protocolRoutingResolvedProbeCount(accountIDs []int64, remediation []ProtocolRoutingRemediation) int {
	unresolved := make(map[int64]struct{}, len(remediation))
	for _, item := range remediation {
		unresolved[item.AccountID] = struct{}{}
	}
	resolved := 0
	for _, accountID := range accountIDs {
		if _, remains := unresolved[accountID]; !remains {
			resolved++
		}
	}
	return resolved
}

func probeProtocolRoutingAccounts(ctx context.Context, prober protocolRoutingCapabilityProber, accountIDs []int64) {
	if prober == nil || len(accountIDs) == 0 {
		return
	}
	workerCount := min(protocolRoutingStartupProbeConcurrency, len(accountIDs))
	jobs := make(chan int64)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for accountID := range jobs {
				if ctx.Err() != nil {
					continue
				}
				prober.ProbeAccountProtocolCapabilitiesForPreparation(ctx, accountID)
			}
		}()
	}
	for _, accountID := range accountIDs {
		if ctx.Err() != nil {
			break
		}
		jobs <- accountID
	}
	close(jobs)
	workers.Wait()
}

func officialSupportedProtocols(account *Account) []protocolrouter.Protocol {
	_, _, profile := protocolAccountEndpoints(account)
	switch profile {
	case protocolrouter.OfficialEndpointAnthropic:
		return []protocolrouter.Protocol{protocolrouter.ProtocolMessages}
	case protocolrouter.OfficialEndpointOpenAICodex:
		return []protocolrouter.Protocol{protocolrouter.ProtocolResponses}
	default:
		return nil
	}
}

func protocolRoutingMigrationModels(account *Account) []string {
	mapping := account.GetModelMapping()
	models := make([]string, 0, len(mapping))
	for requested := range mapping {
		requested = strings.TrimSpace(requested)
		if requested == "" {
			continue
		}
		models = append(models, requested)
	}
	if len(models) == 0 {
		if account.Platform == PlatformAnthropic {
			models = append(models, claude.DefaultTestModel)
		} else {
			models = append(models, openai.DefaultTestModel)
		}
	}
	sort.Strings(models)
	return models
}

func protocolCapabilityHasVerifiedRoutingEvidence(capability *ProtocolEndpointCapability) bool {
	if capability == nil {
		return false
	}
	if capability.IdentityConflict || capability.ProbeEvidence.IdentityConflict {
		return false
	}
	if len(capability.SupportedProtocols) == 0 {
		return false
	}
	if capability.ProbeEvidence.OfficialSeed || capability.ProbeEvidence.InitialProbeCompleted {
		return true
	}
	for _, protocol := range capability.SupportedProtocols {
		if conclusiveProtocolProbeVerdict(capability.ProbeEvidence.Verdicts[string(protocol)]) == ProtocolProbePositive {
			return true
		}
	}
	return false
}

func accountHasLegalProtocolRoute(router *protocolrouter.Router, account *Account, models []string) bool {
	if router == nil {
		return false
	}
	for _, model := range models {
		for _, inbound := range protocolrouter.AllProtocols() {
			request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
				InboundProtocol: inbound,
				RequestedModel:  model,
				ResponsesPath:   protocolrouter.ResponsesPathRoot,
				Profile: protocolrouter.RequestProfile{
					ContentKinds: protocolrouter.ContentText,
				},
				Body: []byte(`{"model":"migration-probe"}`),
			})
			if err != nil {
				continue
			}
			snapshot, err := protocolAccountSnapshotForRequest(account, request)
			if err != nil {
				continue
			}
			if _, err := router.Plan(request, snapshot); err == nil {
				return true
			}
		}
	}
	return false
}

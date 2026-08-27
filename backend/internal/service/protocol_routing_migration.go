package service

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const protocolRoutingStartupProbeConcurrency = 4

type protocolRoutingCapabilityProber interface {
	ProbeAccountProtocolCapabilities(ctx context.Context, accountID int64)
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
	CutoverReady   bool
	Remediation    []ProtocolRoutingRemediation
}

type protocolRoutingMigrationRepository interface {
	ListActive(ctx context.Context) ([]Account, error)
	ProtocolEndpointCapabilityRepository
}

func MigrateProtocolRoutingSSOT(
	ctx context.Context,
	repo protocolRoutingMigrationRepository,
	router *protocolrouter.Router,
) (ProtocolRoutingMigrationReport, error) {
	report := ProtocolRoutingMigrationReport{CutoverReady: true}
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
		official := officialSupportedProtocols(account)
		wasOfficialSeed := false
		if existing, existingErr := repo.GetByKey(ctx, identity.Key()); existingErr == nil && existing != nil {
			wasOfficialSeed = existing.ProbeEvidence.OfficialSeed
		}
		capability, err := repo.EnsureAccountLink(ctx, account, identity, LegacySupportedProtocolsProjection(account), len(official) > 0)
		if err != nil {
			return report, err
		}
		if len(official) > 0 && capability.ProbeEvidence.OfficialSeed && !wasOfficialSeed {
			report.SeededOfficial++
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
			(!capability.ProbeEvidence.InitialProbeCompleted && !capability.ProbeEvidence.OfficialSeed) ||
			len(capability.SupportedProtocols) == 0 {
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
	if initial.CutoverReady || prober == nil || len(initial.Remediation) == 0 {
		return newProtocolRoutingSSOTReady(initial, router), nil
	}

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

	final, err := MigrateProtocolRoutingSSOT(ctx, repo, router)
	final.ProbeAttempts = len(accountIDs)
	final.ProbeResolved = protocolRoutingResolvedProbeCount(accountIDs, final.Remediation)
	if err != nil {
		final.SeededOfficial += initial.SeededOfficial
		return ProtocolRoutingSSOTReady{Report: final}, err
	}
	final.SeededOfficial += initial.SeededOfficial
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
				prober.ProbeAccountProtocolCapabilities(ctx, accountID)
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

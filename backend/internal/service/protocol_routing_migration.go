package service

import (
	"context"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

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
	CutoverReady   bool
	Remediation    []ProtocolRoutingRemediation
}

type protocolRoutingMigrationRepository interface {
	ListActive(ctx context.Context) ([]Account, error)
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
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
		if _, exists := account.Extra[SupportedProtocolsExtraKey]; !exists {
			if protocols := officialSupportedProtocols(account); len(protocols) > 0 {
				if err := ReplaceSupportedProtocols(ctx, repo, account.ID, protocols); err != nil {
					return report, err
				}
				update, _ := BuildSupportedProtocolsUpdate(protocols)
				applySupportedProtocolsUpdate(account, update)
				report.SeededOfficial++
			}
		}
		if len(account.SupportedProtocols()) == 0 {
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
		if requested == "" || strings.Contains(requested, "*") {
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

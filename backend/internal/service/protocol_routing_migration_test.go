package service

import (
	"context"
	"reflect"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

type protocolRoutingMigrationRepo struct {
	accounts []Account
	updates  map[int64]map[string]any
}

func (r *protocolRoutingMigrationRepo) ListActive(context.Context) ([]Account, error) {
	result := make([]Account, len(r.accounts))
	copy(result, r.accounts)
	return result, nil
}

func (r *protocolRoutingMigrationRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	if r.updates == nil {
		r.updates = make(map[int64]map[string]any)
	}
	r.updates[id] = updates
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			applySupportedProtocolsUpdate(&r.accounts[i], updates)
		}
	}
	return nil
}

func TestMigrateProtocolRoutingSSOTSeedsOnlyOfficialProfilesAndReportsCustomAccounts(t *testing.T) {
	repo := &protocolRoutingMigrationRepo{accounts: []Account{
		{ID: 1, Name: "official-openai", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "secret"}},
		{ID: 2, Name: "custom-openai", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "secret", "base_url": "https://relay.example.test/v1"}},
		{ID: 3, Name: "gemini", Platform: PlatformGemini, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "secret"}},
	}}

	report, err := MigrateProtocolRoutingSSOT(context.Background(), repo, NewProtocolRouter())
	if err != nil {
		t.Fatalf("MigrateProtocolRoutingSSOT: %v", err)
	}
	if report.ActiveGoverned != 2 || report.SeededOfficial != 1 || report.CutoverReady {
		t.Fatalf("report = %+v", report)
	}
	if got := repo.updates[1][SupportedProtocolsExtraKey]; !reflect.DeepEqual(got, []string{"responses"}) {
		t.Fatalf("official seed = %#v, want responses", got)
	}
	if _, ok := repo.updates[2]; ok {
		t.Fatal("custom account was inferred instead of reported for probe")
	}
	if len(report.Remediation) != 1 || report.Remediation[0].AccountID != 2 || report.Remediation[0].Reason != ProtocolRoutingRemediationProbeRequired {
		t.Fatalf("remediation = %+v", report.Remediation)
	}

	second, err := MigrateProtocolRoutingSSOT(context.Background(), repo, NewProtocolRouter())
	if err != nil {
		t.Fatalf("second MigrateProtocolRoutingSSOT: %v", err)
	}
	if second.SeededOfficial != 0 {
		t.Fatalf("second seeded official = %d, want 0", second.SeededOfficial)
	}
}

func TestProtocolRoutingMigrationReportRejectsCanonicalAccountWithoutLegalRoute(t *testing.T) {
	repo := &protocolRoutingMigrationRepo{accounts: []Account{{
		ID:       9,
		Name:     "bad-route",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":       "secret",
			"base_url":      "",
			"model_mapping": map[string]any{"client-model": "upstream-model"},
		},
		Extra: map[string]any{SupportedProtocolsExtraKey: []any{string(protocolrouter.ProtocolResponses)}},
	}}}

	report, err := MigrateProtocolRoutingSSOT(context.Background(), repo, NewProtocolRouter())
	if err != nil {
		t.Fatalf("MigrateProtocolRoutingSSOT: %v", err)
	}
	if report.CutoverReady || len(report.Remediation) != 1 || report.Remediation[0].Reason != ProtocolRoutingRemediationNoLegalRoute {
		t.Fatalf("report = %+v", report)
	}
}

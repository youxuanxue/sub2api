package service

import (
	"context"
	"reflect"
	"sync"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

type protocolRoutingMigrationProber struct {
	mu      sync.Mutex
	account map[int64][]protocolrouter.Protocol
	calls   []int64
	repo    *protocolRoutingMigrationRepo
}

func (p *protocolRoutingMigrationProber) ProbeAccountProtocolCapabilities(_ context.Context, accountID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, accountID)
	protocols := p.account[accountID]
	if len(protocols) == 0 || p.repo == nil {
		return
	}
	update, err := BuildSupportedProtocolsUpdate(protocols)
	if err != nil {
		return
	}
	_ = p.repo.UpdateExtra(context.Background(), accountID, update)
}

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

func TestMigrateProtocolRoutingSSOTExcludesVideoOnlyAccounts(t *testing.T) {
	repo := &protocolRoutingMigrationRepo{accounts: []Account{{
		ID:          96,
		Name:        "xrtoken",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeDoubaoVideo,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": newapiintegration.XRTokenBaseURL,
			"model_mapping": map[string]any{
				"doubao-seedance-2-0-260128": "doubao-seedance-2-0-260128",
			},
		},
	}}}

	report, err := MigrateProtocolRoutingSSOT(context.Background(), repo, NewProtocolRouter())
	if err != nil {
		t.Fatalf("MigrateProtocolRoutingSSOT: %v", err)
	}
	if report.ActiveGoverned != 0 || !report.CutoverReady || len(report.Remediation) != 0 {
		t.Fatalf("video-only account entered text protocol governance: %+v", report)
	}
}

func TestProtocolRoutingMediaOnlyClassificationKeepsTextWildcardAccounts(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-*":       "gpt-*",
				"gpt-image-1": "gpt-image-1",
			},
		},
	}

	if protocolRoutingAccountIsMediaOnly(account) {
		t.Fatal("text wildcard plus image model was misclassified as media-only")
	}
}

func TestProtocolRoutingMediaOnlyClassificationExcludesKnownImageAliases(t *testing.T) {
	for _, model := range []string{
		"grok-imagine",
		"grok-imagine-edit",
		"gemini-3.1-flash-image-preview",
	} {
		account := &Account{
			Platform: PlatformNewAPI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"model_mapping": map[string]any{model: model},
			},
		}

		if !protocolRoutingAccountIsMediaOnly(account) {
			t.Fatalf("model %q was not classified as media-only", model)
		}
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

func TestProtocolRoutingMigrationUsesWildcardServedModelsForLegalRoute(t *testing.T) {
	repo := &protocolRoutingMigrationRepo{accounts: []Account{{
		ID:       95,
		Name:     "cloudwise",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://api.cloudwise.ai/api",
			"model_mapping": map[string]any{
				"claude-*": "claude-*",
				"kimi-*":   "kimi-*",
			},
		},
		Extra: map[string]any{SupportedProtocolsExtraKey: []any{string(protocolrouter.ProtocolMessages)}},
	}}}

	report, err := MigrateProtocolRoutingSSOT(context.Background(), repo, NewProtocolRouter())
	if err != nil {
		t.Fatalf("MigrateProtocolRoutingSSOT: %v", err)
	}
	if !report.CutoverReady || len(report.Remediation) != 0 {
		t.Fatalf("wildcard served-model rules were replaced by an unrelated default: %+v", report)
	}
}

func TestPrepareProtocolRoutingSSOTProbesRemediationBeforeEnablingRouter(t *testing.T) {
	repo := &protocolRoutingMigrationRepo{accounts: []Account{{
		ID:       12,
		Name:     "custom-openai",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://relay.example.test/v1",
		},
	}}}
	prober := &protocolRoutingMigrationProber{
		repo: repo,
		account: map[int64][]protocolrouter.Protocol{
			12: {protocolrouter.ProtocolChatCompletions},
		},
	}
	router := NewProtocolRouter()

	ready, err := prepareProtocolRoutingSSOT(context.Background(), repo, router, prober)
	if err != nil {
		t.Fatalf("prepareProtocolRoutingSSOT: %v", err)
	}
	if !ready.Report.CutoverReady || ready.EnabledRouter() != router {
		t.Fatalf("ready = %+v router=%p, want cutover-ready router %p", ready.Report, ready.EnabledRouter(), router)
	}
	if ready.Report.ProbeAttempts != 1 || ready.Report.ProbeResolved != 1 {
		t.Fatalf("probe outcome = attempts:%d resolved:%d, want 1/1", ready.Report.ProbeAttempts, ready.Report.ProbeResolved)
	}
	if !reflect.DeepEqual(prober.calls, []int64{12}) {
		t.Fatalf("probe calls = %v, want [12]", prober.calls)
	}
	if got := repo.accounts[0].SupportedProtocols(); !reflect.DeepEqual(got, []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions}) {
		t.Fatalf("supported protocols = %v, want chat_completions", got)
	}
}

func TestPrepareProtocolRoutingSSOTKeepsLegacyRoutingWhenRemediationRemains(t *testing.T) {
	repo := &protocolRoutingMigrationRepo{accounts: []Account{{
		ID:       13,
		Name:     "unresolved-openai",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://relay.example.test/v1",
		},
	}}}
	prober := &protocolRoutingMigrationProber{repo: repo, account: map[int64][]protocolrouter.Protocol{}}

	ready, err := prepareProtocolRoutingSSOT(context.Background(), repo, NewProtocolRouter(), prober)
	if err != nil {
		t.Fatalf("prepareProtocolRoutingSSOT: %v", err)
	}
	if ready.Report.CutoverReady || ready.EnabledRouter() != nil {
		t.Fatalf("ready = %+v router=%p, want remediation with legacy routing", ready.Report, ready.EnabledRouter())
	}
	if ready.Report.ProbeAttempts != 1 || ready.Report.ProbeResolved != 0 {
		t.Fatalf("probe outcome = attempts:%d resolved:%d, want 1/0", ready.Report.ProbeAttempts, ready.Report.ProbeResolved)
	}
	if !reflect.DeepEqual(prober.calls, []int64{13}) {
		t.Fatalf("probe calls = %v, want [13]", prober.calls)
	}
}

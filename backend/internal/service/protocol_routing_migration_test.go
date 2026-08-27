package service

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

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
	capability, err := p.repo.GetByAccountID(context.Background(), accountID)
	if err != nil {
		return
	}
	lease, acquired, err := p.repo.AcquireProbeLease(context.Background(), capability.CapabilityKey, "test-prober", time.Now(), time.Minute)
	if err != nil || !acquired {
		return
	}
	_, _, _ = p.repo.CommitProbeResult(context.Background(), lease, ProtocolCapabilityMutation{
		SupportedProtocols: protocols, InitialProbeCompleted: true,
	})
}

type protocolRoutingMigrationRepo struct {
	accounts     []Account
	updates      map[int64]map[string]any
	capabilities map[string]*ProtocolEndpointCapability
	links        map[int64]string
}

func (r *protocolRoutingMigrationRepo) ListActive(context.Context) ([]Account, error) {
	result := make([]Account, len(r.accounts))
	copy(result, r.accounts)
	for i := range result {
		if result[i].Status == "" {
			result[i].Status = StatusActive
			result[i].Schedulable = true
		}
		if key := r.links[result[i].ID]; key != "" {
			capability := r.capabilities[key]
			result[i].ProtocolEndpointCapability = capability
			result[i].ProtocolEndpointCapabilityID = &capability.ID
		}
	}
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

func (r *protocolRoutingMigrationRepo) ensureCapabilityMaps() {
	if r.capabilities == nil {
		r.capabilities = make(map[string]*ProtocolEndpointCapability)
	}
	if r.links == nil {
		r.links = make(map[int64]string)
	}
}

func (r *protocolRoutingMigrationRepo) EnsureAccountLink(_ context.Context, account *Account, identity ProtocolEndpointIdentity, historical []protocolrouter.Protocol, official bool) (*ProtocolEndpointCapability, error) {
	r.ensureCapabilityMaps()
	key := identity.Key()
	capability := r.capabilities[key]
	if capability == nil {
		capability = &ProtocolEndpointCapability{ID: int64(len(r.capabilities) + 1), CapabilityKey: key, Identity: identity, Revision: 1}
		r.capabilities[key] = capability
	}
	merged, err := NormalizeSupportedProtocols(append(append([]protocolrouter.Protocol{}, capability.SupportedProtocols...), historical...))
	if err != nil {
		return nil, err
	}
	if !protocolListsEqual(merged, capability.SupportedProtocols) {
		capability.SupportedProtocols = merged
		capability.Revision++
	}
	if official {
		capability.ProbeEvidence.OfficialSeed = true
		capability.ProbeEvidence.InitialProbeCompleted = true
		capability.SupportedProtocols, _ = NormalizeSupportedProtocols(append(capability.SupportedProtocols, officialSupportedProtocols(account)...))
	}
	r.links[account.ID] = key
	account.ProtocolEndpointCapability = capability
	account.ProtocolEndpointCapabilityID = &capability.ID
	for i := range r.accounts {
		if r.accounts[i].ID == account.ID {
			r.accounts[i].ProtocolEndpointCapability = capability
			r.accounts[i].ProtocolEndpointCapabilityID = &capability.ID
			update, _ := BuildSupportedProtocolsUpdate(capability.SupportedProtocols)
			applySupportedProtocolsUpdate(&r.accounts[i], update)
		}
	}
	return capability, nil
}

func (r *protocolRoutingMigrationRepo) GetByAccountID(_ context.Context, accountID int64) (*ProtocolEndpointCapability, error) {
	r.ensureCapabilityMaps()
	capability := r.capabilities[r.links[accountID]]
	if capability == nil {
		return nil, ErrProtocolCapabilityNotFound
	}
	return capability, nil
}

func (r *protocolRoutingMigrationRepo) GetByKey(_ context.Context, key string) (*ProtocolEndpointCapability, error) {
	r.ensureCapabilityMaps()
	if capability := r.capabilities[key]; capability != nil {
		return capability, nil
	}
	return nil, ErrProtocolCapabilityNotFound
}

func (r *protocolRoutingMigrationRepo) ListLinkedAccountIDs(_ context.Context, key string) ([]int64, error) {
	ids := make([]int64, 0)
	for id, linked := range r.links {
		if linked == key {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (r *protocolRoutingMigrationRepo) AcquireProbeLease(_ context.Context, key, owner string, _ time.Time, _ time.Duration) (ProtocolProbeLease, bool, error) {
	capability, err := r.GetByKey(context.Background(), key)
	if err != nil {
		return ProtocolProbeLease{}, false, err
	}
	if capability.ProbeLeaseOwner != nil {
		return ProtocolProbeLease{}, false, nil
	}
	capability.ProbeGeneration++
	capability.ProbeLeaseOwner = &owner
	return ProtocolProbeLease{CapabilityKey: key, Generation: capability.ProbeGeneration, Revision: capability.Revision, Owner: owner}, true, nil
}

func (r *protocolRoutingMigrationRepo) CommitProbeResult(_ context.Context, lease ProtocolProbeLease, mutation ProtocolCapabilityMutation) (*ProtocolEndpointCapability, int, error) {
	capability, err := r.GetByKey(context.Background(), lease.CapabilityKey)
	if err != nil {
		return nil, 0, err
	}
	if capability.Revision != lease.Revision || capability.ProbeGeneration != lease.Generation || capability.ProbeLeaseOwner == nil || *capability.ProbeLeaseOwner != lease.Owner {
		return nil, 0, ErrProtocolCapabilityStaleWrite
	}
	if mutation.SupportedProtocols == nil {
		return nil, 0, errors.New("test mutation requires protocols")
	}
	capability.SupportedProtocols, _ = NormalizeSupportedProtocols(mutation.SupportedProtocols)
	capability.Revision++
	capability.ProbeEvidence.InitialProbeCompleted = mutation.InitialProbeCompleted
	capability.ProbeLeaseOwner = nil
	affected := 0
	for i := range r.accounts {
		if r.links[r.accounts[i].ID] == lease.CapabilityKey {
			r.accounts[i].ProtocolEndpointCapability = capability
			update, _ := BuildSupportedProtocolsUpdate(capability.SupportedProtocols)
			applySupportedProtocolsUpdate(&r.accounts[i], update)
			affected++
		}
	}
	return capability, affected, nil
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
	if got := repo.accounts[0].Extra[SupportedProtocolsExtraKey]; !reflect.DeepEqual(got, []string{"responses"}) {
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

	if protocolRoutingAccountHasNoTextModels(account) {
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

		if !protocolRoutingAccountHasNoTextModels(account) {
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

func TestProtocolRoutingMigrationDoesNotTreatHistoricalWildcardSeedAsVerified(t *testing.T) {
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
	if report.CutoverReady || len(report.Remediation) != 1 || report.Remediation[0].Reason != ProtocolRoutingRemediationProbeRequired {
		t.Fatalf("historical positive seed bypassed initial endpoint probe: %+v", report)
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

func TestPrepareProtocolRoutingSSOTKeepsRouterAndFailsReadinessWhenRemediationRemains(t *testing.T) {
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

	router := NewProtocolRouter()
	ready, err := prepareProtocolRoutingSSOT(context.Background(), repo, router, prober)
	if err != nil {
		t.Fatalf("prepareProtocolRoutingSSOT: %v", err)
	}
	if ready.Report.CutoverReady || ready.EnabledRouter() != router {
		t.Fatalf("ready = %+v router=%p, want remediation with router %p", ready.Report, ready.EnabledRouter(), router)
	}
	if ready.Ready() {
		t.Fatal("Ready() = true, want false while remediation remains")
	}
	if ready.Report.ProbeAttempts != 1 || ready.Report.ProbeResolved != 0 {
		t.Fatalf("probe outcome = attempts:%d resolved:%d, want 1/0", ready.Report.ProbeAttempts, ready.Report.ProbeResolved)
	}
	if !reflect.DeepEqual(prober.calls, []int64{13}) {
		t.Fatalf("probe calls = %v, want [13]", prober.calls)
	}
}

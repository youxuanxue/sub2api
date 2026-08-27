package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

type protocolProbeCASRepo struct {
	AccountRepository
	mu          sync.Mutex
	account     *Account
	updateCalls int
}

func (r *protocolProbeCASRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	return cloneProtocolProbeAccount(r.account), nil
}

func (r *protocolProbeCASRepo) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if r.account != nil && r.account.ID == id {
			result = append(result, cloneProtocolProbeAccount(r.account))
		}
	}
	return result, nil
}

func (r *protocolProbeCASRepo) EnsureAccountLink(_ context.Context, account *Account, identity ProtocolEndpointIdentity, historical []protocolrouter.Protocol, official bool) (*ProtocolEndpointCapability, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != account.ID {
		return nil, ErrAccountNotFound
	}
	if r.account.ProtocolEndpointCapability == nil {
		id := int64(1)
		r.account.ProtocolEndpointCapabilityID = &id
		r.account.ProtocolEndpointCapability = &ProtocolEndpointCapability{ID: id, CapabilityKey: identity.Key(), Identity: identity, Revision: 1}
	}
	capability := r.account.ProtocolEndpointCapability
	capability.SupportedProtocols, _ = NormalizeSupportedProtocols(append(capability.SupportedProtocols, historical...))
	if official {
		capability.SupportedProtocols, _ = NormalizeSupportedProtocols(append(capability.SupportedProtocols, officialSupportedProtocols(account)...))
		capability.ProbeEvidence.OfficialSeed = true
		capability.ProbeEvidence.InitialProbeCompleted = true
	}
	account.ProtocolEndpointCapabilityID = r.account.ProtocolEndpointCapabilityID
	account.ProtocolEndpointCapability = capability
	return capability, nil
}

func (r *protocolProbeCASRepo) GetByAccountID(_ context.Context, accountID int64) (*ProtocolEndpointCapability, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != accountID || r.account.ProtocolEndpointCapability == nil {
		return nil, ErrProtocolCapabilityNotFound
	}
	return r.account.ProtocolEndpointCapability, nil
}

func (r *protocolProbeCASRepo) GetByKey(_ context.Context, key string) (*ProtocolEndpointCapability, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ProtocolEndpointCapability == nil || r.account.ProtocolEndpointCapability.CapabilityKey != key {
		return nil, ErrProtocolCapabilityNotFound
	}
	return r.account.ProtocolEndpointCapability, nil
}

func (r *protocolProbeCASRepo) ListLinkedAccountIDs(_ context.Context, key string) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account != nil && r.account.ProtocolEndpointCapability != nil && r.account.ProtocolEndpointCapability.CapabilityKey == key {
		return []int64{r.account.ID}, nil
	}
	return nil, nil
}

func (r *protocolProbeCASRepo) AcquireProbeLease(_ context.Context, key, owner string, _ time.Time, _ time.Duration) (ProtocolProbeLease, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ProtocolEndpointCapability == nil || r.account.ProtocolEndpointCapability.CapabilityKey != key {
		return ProtocolProbeLease{}, false, ErrProtocolCapabilityNotFound
	}
	capability := r.account.ProtocolEndpointCapability
	if capability.ProbeLeaseOwner != nil {
		return ProtocolProbeLease{}, false, nil
	}
	capability.ProbeGeneration++
	capability.ProbeLeaseOwner = &owner
	return ProtocolProbeLease{CapabilityKey: key, Generation: capability.ProbeGeneration, Revision: capability.Revision, Owner: owner}, true, nil
}

func (r *protocolProbeCASRepo) CommitProbeResult(_ context.Context, lease ProtocolProbeLease, mutation ProtocolCapabilityMutation) (*ProtocolEndpointCapability, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	capability := r.account.ProtocolEndpointCapability
	if capability == nil || capability.CapabilityKey != lease.CapabilityKey || capability.Revision != lease.Revision || capability.ProbeGeneration != lease.Generation || capability.ProbeLeaseOwner == nil || *capability.ProbeLeaseOwner != lease.Owner {
		return nil, 0, ErrProtocolCapabilityStaleWrite
	}
	normalized, err := NormalizeSupportedProtocols(mutation.SupportedProtocols)
	if err != nil {
		return nil, 0, err
	}
	if !protocolListsEqual(capability.SupportedProtocols, normalized) || capability.IdentityConflict != mutation.IdentityConflict {
		capability.Revision++
	}
	capability.SupportedProtocols = normalized
	capability.ProbeEvidence = mutation.ProbeEvidence
	capability.ProbeEvidence.InitialProbeCompleted = mutation.InitialProbeCompleted || capability.ProbeEvidence.InitialProbeCompleted
	capability.IdentityConflict = mutation.IdentityConflict
	capability.ProbeLeaseOwner = nil
	update, _ := BuildSupportedProtocolsUpdate(normalized)
	applySupportedProtocolsUpdate(r.account, update)
	r.updateCalls++
	return capability, 1, nil
}

type protocolProbeSetUpstream struct {
	mu             sync.Mutex
	paths          []string
	authorizations []string
	profiles       []HTTPUpstreamProfile
	redirectsOff   []bool
	grokVersions   []string
	originators    []string
	codexWindows   []string
}

type protocolProbeBarrierUpstream struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	started     int
	allStarted  chan struct{}
	release     chan struct{}
	wantStarted int
}

func (u *protocolProbeBarrierUpstream) Do(
	req *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (u *protocolProbeBarrierUpstream) DoWithTLS(
	req *http.Request,
	_ string,
	_ int64,
	_ int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	u.mu.Lock()
	u.inFlight++
	if u.inFlight > u.maxInFlight {
		u.maxInFlight = u.inFlight
	}
	u.started++
	if u.started == u.wantStarted {
		close(u.allStarted)
	}
	u.mu.Unlock()

	select {
	case <-u.release:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}

	u.mu.Lock()
	u.inFlight--
	u.mu.Unlock()

	body := `{"output":[{"type":"function_call","name":"probe_ping"}]}`
	switch req.URL.Path {
	case "/v1/messages":
		body = `{"type":"message","content":[{"type":"text","text":"OK"}]}`
	case "/v1/chat/completions":
		body = `{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (u *protocolProbeSetUpstream) Do(
	req *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (u *protocolProbeSetUpstream) DoWithTLS(
	req *http.Request,
	_ string,
	_ int64,
	_ int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	u.mu.Lock()
	u.paths = append(u.paths, req.URL.Path)
	u.authorizations = append(u.authorizations, getHeaderRaw(req.Header, "authorization"))
	u.profiles = append(u.profiles, HTTPUpstreamProfileFromContext(req.Context()))
	u.redirectsOff = append(u.redirectsOff, HTTPUpstreamRedirectsDisabled(req.Context()))
	u.grokVersions = append(u.grokVersions, req.Header.Get("X-Grok-Client-Version"))
	u.originators = append(u.originators, req.Header.Get("Originator"))
	u.codexWindows = append(u.codexWindows, req.Header.Get("X-Codex-Window-ID"))
	u.mu.Unlock()

	body := `{"output":[{"type":"function_call","name":"probe_ping"}]}`
	switch req.URL.Path {
	case "/v1/messages":
		body = `{"type":"message","content":[{"type":"text","text":"OK"}]}`
	case "/v1/chat/completions":
		body = `{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func cloneProtocolProbeAccount(account *Account) *Account {
	clone := *account
	clone.Credentials = make(map[string]any, len(account.Credentials))
	for key, value := range account.Credentials {
		clone.Credentials[key] = value
	}
	clone.Extra = make(map[string]any, len(account.Extra))
	for key, value := range account.Extra {
		switch typed := value.(type) {
		case []string:
			clone.Extra[key] = append([]string(nil), typed...)
		case []any:
			clone.Extra[key] = append([]any(nil), typed...)
		default:
			clone.Extra[key] = value
		}
	}
	return &clone
}

func TestApplyProtocolProbeVerdictsUpdatesOnlyConclusiveEndpointFacts(t *testing.T) {
	prior := []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses}
	got, err := ApplyProtocolProbeVerdicts(prior, map[protocolrouter.Protocol]ProtocolProbeVerdict{
		protocolrouter.ProtocolMessages:        ProtocolProbeEndpointNegative,
		protocolrouter.ProtocolChatCompletions: ProtocolProbePositive,
		protocolrouter.ProtocolResponses:       ProtocolProbeInconclusive,
	})
	if err != nil {
		t.Fatalf("ApplyProtocolProbeVerdicts: %v", err)
	}
	want := []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("protocols = %v, want %v", got, want)
	}

	got, err = ApplyProtocolProbeVerdicts(got, map[protocolrouter.Protocol]ProtocolProbeVerdict{
		protocolrouter.ProtocolResponses: ProtocolProbeModelSpecific,
	})
	if err != nil {
		t.Fatalf("ApplyProtocolProbeVerdicts model-specific: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("model-specific verdict changed endpoint fact: got %v want %v", got, want)
	}
}

func TestResolveProtocolProbeGenerationFailsClosedOnContradictoryEvidence(t *testing.T) {
	resolution, err := resolveProtocolProbeGeneration(
		[]protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses},
		false,
		nil,
		[]protocolrouter.Protocol{protocolrouter.ProtocolResponses},
		map[protocolrouter.Protocol][]ProtocolProbeVerdict{
			protocolrouter.ProtocolResponses: {ProtocolProbePositive, ProtocolProbeEndpointNegative},
		},
	)
	if err != nil {
		t.Fatalf("resolveProtocolProbeGeneration: %v", err)
	}
	if !resolution.IdentityConflict {
		t.Fatal("contradictory endpoint evidence did not mark identity conflict")
	}
	if slices.Contains(resolution.SupportedProtocols, protocolrouter.ProtocolResponses) {
		t.Fatalf("conflicted protocol remained routable: %v", resolution.SupportedProtocols)
	}
	if resolution.AllConclusive {
		t.Fatal("conflicted generation was marked conclusive")
	}
}

func TestResolveProtocolProbeGenerationClearsConflictAfterConsistentProbe(t *testing.T) {
	resolution, err := resolveProtocolProbeGeneration(
		[]protocolrouter.Protocol{protocolrouter.ProtocolMessages},
		true,
		map[string]any{string(protocolrouter.ProtocolResponses): "conflict"},
		[]protocolrouter.Protocol{protocolrouter.ProtocolResponses},
		map[protocolrouter.Protocol][]ProtocolProbeVerdict{
			protocolrouter.ProtocolResponses: {ProtocolProbePositive, ProtocolProbePositive},
		},
	)
	if err != nil {
		t.Fatalf("resolveProtocolProbeGeneration: %v", err)
	}
	if resolution.IdentityConflict {
		t.Fatal("consistent conclusive generation did not clear identity conflict")
	}
	if !resolution.AllConclusive {
		t.Fatal("consistent positive evidence was not conclusive")
	}
	if !slices.Contains(resolution.SupportedProtocols, protocolrouter.ProtocolResponses) {
		t.Fatalf("positive protocol was not restored: %v", resolution.SupportedProtocols)
	}
}

func TestResolveProtocolProbeGenerationPreservesConflictOnInconclusiveProbe(t *testing.T) {
	resolution, err := resolveProtocolProbeGeneration(
		[]protocolrouter.Protocol{protocolrouter.ProtocolMessages},
		true,
		map[string]any{string(protocolrouter.ProtocolResponses): string(ProtocolProbePositive)},
		[]protocolrouter.Protocol{protocolrouter.ProtocolResponses},
		map[protocolrouter.Protocol][]ProtocolProbeVerdict{
			protocolrouter.ProtocolResponses: {ProtocolProbeModelSpecific, ProtocolProbeInconclusive},
		},
	)
	if err != nil {
		t.Fatalf("resolveProtocolProbeGeneration: %v", err)
	}
	if !resolution.IdentityConflict {
		t.Fatal("inconclusive generation cleared an existing identity conflict")
	}
	if resolution.AllConclusive {
		t.Fatal("model-specific/inconclusive evidence was marked conclusive")
	}
}

func TestResolveProtocolProbeGenerationConflictsWithPriorConclusiveEvidence(t *testing.T) {
	tests := []struct {
		name         string
		prior        []protocolrouter.Protocol
		priorVerdict ProtocolProbeVerdict
		observed     ProtocolProbeVerdict
	}{
		{
			name:         "positive then endpoint negative",
			prior:        []protocolrouter.Protocol{protocolrouter.ProtocolResponses},
			priorVerdict: ProtocolProbePositive,
			observed:     ProtocolProbeEndpointNegative,
		},
		{
			name:         "endpoint negative then positive",
			priorVerdict: ProtocolProbeEndpointNegative,
			observed:     ProtocolProbePositive,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolution, err := resolveProtocolProbeGeneration(
				tt.prior,
				false,
				map[string]any{string(protocolrouter.ProtocolResponses): string(tt.priorVerdict)},
				[]protocolrouter.Protocol{protocolrouter.ProtocolResponses},
				map[protocolrouter.Protocol][]ProtocolProbeVerdict{
					protocolrouter.ProtocolResponses: {tt.observed},
				},
			)
			if err != nil {
				t.Fatalf("resolveProtocolProbeGeneration: %v", err)
			}
			if !resolution.IdentityConflict {
				t.Fatal("opposite conclusive generations did not mark identity conflict")
			}
			if slices.Contains(resolution.SupportedProtocols, protocolrouter.ProtocolResponses) {
				t.Fatalf("conflicted protocol remained routable: %v", resolution.SupportedProtocols)
			}
		})
	}
}

func TestResolveProtocolProbeGenerationKeepsConclusiveHistoryAcrossInconclusiveGeneration(t *testing.T) {
	protocol := protocolrouter.ProtocolResponses
	tests := []struct {
		name           string
		priorProtocols []protocolrouter.Protocol
		priorVerdict   ProtocolProbeVerdict
		middleVerdict  ProtocolProbeVerdict
		finalVerdict   ProtocolProbeVerdict
	}{
		{
			name:           "positive through inconclusive to endpoint negative",
			priorProtocols: []protocolrouter.Protocol{protocol},
			priorVerdict:   ProtocolProbePositive,
			middleVerdict:  ProtocolProbeInconclusive,
			finalVerdict:   ProtocolProbeEndpointNegative,
		},
		{
			name:          "endpoint negative through model specific to positive",
			priorVerdict:  ProtocolProbeEndpointNegative,
			middleVerdict: ProtocolProbeModelSpecific,
			finalVerdict:  ProtocolProbePositive,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			middle, err := resolveProtocolProbeGeneration(
				tt.priorProtocols,
				false,
				map[string]any{string(protocol): string(tt.priorVerdict)},
				[]protocolrouter.Protocol{protocol},
				map[protocolrouter.Protocol][]ProtocolProbeVerdict{protocol: {tt.middleVerdict}},
			)
			if err != nil {
				t.Fatalf("resolve intermediate generation: %v", err)
			}
			if got := conclusiveProtocolProbeVerdict(middle.Evidence[protocol]); got != tt.priorVerdict {
				t.Fatalf("persisted verdict after intermediate generation = %q, want %q", got, tt.priorVerdict)
			}

			final, err := resolveProtocolProbeGeneration(
				middle.SupportedProtocols,
				middle.IdentityConflict,
				map[string]any{string(protocol): middle.Evidence[protocol]},
				[]protocolrouter.Protocol{protocol},
				map[protocolrouter.Protocol][]ProtocolProbeVerdict{protocol: {tt.finalVerdict}},
			)
			if err != nil {
				t.Fatalf("resolve opposite conclusive generation: %v", err)
			}
			if !final.IdentityConflict || slices.Contains(final.SupportedProtocols, protocol) {
				t.Fatalf("conclusive history did not fail closed: conflict=%t protocols=%v", final.IdentityConflict, final.SupportedProtocols)
			}
		})
	}
}

func TestProbeProtocolWitnessesStopsAfterFirstConclusiveVerdict(t *testing.T) {
	witnesses := []*Account{{ID: 1}, {ID: 2}, {ID: 3}}
	calls := 0
	verdicts := probeProtocolWitnesses(witnesses, func(witness *Account) (protocolProbeObservation, bool) {
		calls++
		if witness.ID == 1 {
			return protocolProbeObservation{verdict: ProtocolProbePositive}, true
		}
		return protocolProbeObservation{verdict: ProtocolProbeEndpointNegative}, true
	})
	if calls != 1 {
		t.Fatalf("witness calls = %d, want 1", calls)
	}
	if !reflect.DeepEqual(verdicts, []ProtocolProbeVerdict{ProtocolProbePositive}) {
		t.Fatalf("verdicts = %v, want first conclusive verdict only", verdicts)
	}
}

func TestSelectProtocolProbeWitnessesFiltersUnusableAuthorizationBeforeBound(t *testing.T) {
	now := time.Now().UTC()
	validVertexCredential := `{"type":"service_account","project_id":"vertex-project","private_key":"private-key","client_email":"svc@vertex-project.iam.gserviceaccount.com"}`
	accounts := []*Account{
		{ID: 1, Priority: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"base_url": "https://relay.example.test/v1"}},
		{ID: 2, Priority: 2, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "expired", "expires_at": now.Add(-time.Minute).Format(time.RFC3339)}},
		{ID: 3, Priority: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "   "}},
		{ID: 4, Priority: 4, Platform: PlatformNewAPI, Type: AccountTypeServiceAccount, ChannelType: newapiconstant.ChannelTypeVertexAi, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"service_account_json": `{"project_id":"vertex-project"}`}},
		{ID: 5, Priority: 5, Platform: PlatformNewAPI, Type: AccountTypeServiceAccount, ChannelType: newapiconstant.ChannelTypeVertexAi, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"service_account_json": validVertexCredential}},
		{ID: 6, Priority: 6, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"api_key": "usable"}},
		{ID: 7, Priority: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "oauth-usable"}},
	}

	witnesses := selectProtocolProbeWitnesses(accounts)
	if got := protocolProbeWitnessIDs(witnesses); !reflect.DeepEqual(got, []int64{5, 6, 7}) {
		t.Fatalf("witness ids = %v, want [5 6 7]", got)
	}
}

func protocolProbeWitnessIDs(accounts []*Account) []int64 {
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}

func TestProbeAccountProtocolCapabilitiesNoWitnessPreservesConflict(t *testing.T) {
	account := protocolRoutingOpenAIAccount(92)
	account.Status = StatusError
	account.Schedulable = false
	account.ErrorMessage = "authorization unavailable"
	identity, governed, err := BuildProtocolEndpointIdentity(account)
	if err != nil || !governed {
		t.Fatalf("BuildProtocolEndpointIdentity = governed:%v err:%v", governed, err)
	}
	capabilityID := int64(1)
	account.ProtocolEndpointCapabilityID = &capabilityID
	account.ProtocolEndpointCapability = &ProtocolEndpointCapability{
		ID:                 capabilityID,
		CapabilityKey:      identity.Key(),
		Identity:           identity,
		SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolResponses},
		ProbeEvidence: ProtocolProbeEvidence{
			IdentityConflict: true,
			Verdicts:         map[string]any{string(protocolrouter.ProtocolResponses): "conflict"},
		},
		IdentityConflict: true,
		Revision:         2,
	}
	repo := &protocolProbeCASRepo{account: cloneProtocolProbeAccount(account)}
	svc := &AccountTestService{accountRepo: repo, protocolCapabilityRepo: repo}

	result, err := svc.ProbeAccountProtocolCapabilitiesNow(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("ProbeAccountProtocolCapabilitiesNow: %v", err)
	}
	if result.Outcome != ProtocolProbeRunInconclusive || result.Reason != "no_usable_witness" {
		t.Fatalf("result = %#v", result)
	}
	if result.Capability == nil || !result.Capability.IdentityConflict || !result.Capability.ProbeEvidence.IdentityConflict {
		t.Fatalf("no-witness probe cleared conflict: %#v", result.Capability)
	}
}

func TestProtocolProbeCandidatesCoverGovernedCustomAccountsOnly(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    []protocolrouter.Protocol
	}{
		{
			name: "custom anthropic base probes all declared text endpoints",
			account: &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "secret", "base_url": "https://relay.example.test/v1",
			}},
			want: []protocolrouter.Protocol{
				protocolrouter.ProtocolMessages,
				protocolrouter.ProtocolChatCompletions,
				protocolrouter.ProtocolResponses,
			},
		},
		{
			name: "custom newapi per-protocol base probes only declared endpoints",
			account: &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "secret",
				"api_base_urls": map[string]any{
					APIProtocolChatCompletions: "https://chat.example.test/v1",
					APIProtocolResponses:       "https://responses.example.test/v1",
				},
			}},
			want: []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses},
		},
		{
			name: "official openai oauth is seeded without probe",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
				"access_token": "secret",
			}},
			want: nil,
		},
		{
			name: "custom anthropic oauth probes only its messages endpoint",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
				Credentials: map[string]any{
					"access_token": "secret",
				},
				Extra: map[string]any{
					"custom_base_url_enabled": true,
					"custom_base_url":         "https://oauth-relay.example.test",
				},
			},
			want: []protocolrouter.Protocol{protocolrouter.ProtocolMessages},
		},
		{
			name: "grok oauth probes its explicit text endpoints",
			account: &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
				"access_token": "oauth-secret", "base_url": "https://grok.example.test/v1",
			}},
			want: []protocolrouter.Protocol{
				protocolrouter.ProtocolMessages,
				protocolrouter.ProtocolChatCompletions,
				protocolrouter.ProtocolResponses,
			},
		},
		{
			name: "ungoverned gemini is excluded",
			account: &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "secret", "base_url": "https://gemini.example.test",
			}},
			want: nil,
		},
		{
			name: "antigravity oauth probes provider specific gemini only",
			account: &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{
				"access_token": "secret", "project_id": "project-a",
			}},
			want: []protocolrouter.Protocol{protocolrouter.ProtocolGeminiGenerateContent},
		},
		{
			name: "antigravity edge relay probes its configurable text endpoints",
			account: &Account{Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "secret", "base_url": "https://api-us3.tokenkey.dev",
			}},
			want: []protocolrouter.Protocol{
				protocolrouter.ProtocolMessages,
				protocolrouter.ProtocolChatCompletions,
				protocolrouter.ProtocolResponses,
			},
		},
		{
			name: "arbitrary antigravity apikey endpoint is not a governed relay",
			account: &Account{Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "secret", "base_url": "https://relay.example.test",
			}},
			want: nil,
		},
		{
			name: "exact newapi vertex service account probes provider specific gemini only",
			account: &Account{Platform: PlatformNewAPI, Type: AccountTypeServiceAccount,
				ChannelType: newapiconstant.ChannelTypeVertexAi,
				Credentials: map[string]any{"project_id": "project-v"}},
			want: []protocolrouter.Protocol{protocolrouter.ProtocolGeminiGenerateContent},
		},
		{
			name: "embedding only mapping is excluded from text protocol probes",
			account: &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "secret", "base_url": "https://embedding.example.test/v1",
				"model_mapping": map[string]any{"embedding": "bge-large-en"},
			}},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProtocolProbeCandidates(tt.account); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ProtocolProbeCandidates = %v, want %v", got, tt.want)
			}
		})
	}
}

func installOpaqueNonTextProtocolProbePricing(t *testing.T) {
	t.Helper()
	tkOverlayMu.Lock()
	previous := tkOverlayEffective
	tkOverlayEffective = &tkPricingOverlaySnapshot{Models: map[string]*LiteLLMModelPricing{
		"opaque-vector-v1": {Mode: "embedding"},
		"text-chat-v1":     {Mode: "chat"},
	}}
	tkOverlayMu.Unlock()
	t.Cleanup(func() {
		tkOverlayMu.Lock()
		tkOverlayEffective = previous
		tkOverlayMu.Unlock()
	})
}

func TestSelectProtocolProbeModelUsesRegistryModeForOpaqueNonTextModels(t *testing.T) {
	installOpaqueNonTextProtocolProbePricing(t)
	mixed := &Account{
		Platform: PlatformNewAPI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"vector": "opaque-vector-v1",
				"chat":   "text-chat-v1",
			},
		},
	}
	if got := selectProtocolProbeModel(mixed); got != "text-chat-v1" {
		t.Fatalf("selectProtocolProbeModel = %q, want text-chat-v1", got)
	}
}

func TestProtocolProbeCandidatesUseRegistryModeForOpaqueNonTextAccounts(t *testing.T) {
	installOpaqueNonTextProtocolProbePricing(t)
	nonTextOnly := &Account{
		Platform: PlatformNewAPI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":       "secret",
			"base_url":      "https://vector.example.test/v1",
			"model_mapping": map[string]any{"vector": "opaque-vector-v1"},
		},
	}
	if got := ProtocolProbeCandidates(nonTextOnly); got != nil {
		t.Fatalf("ProtocolProbeCandidates = %v, want nil for registry-classified non-text account", got)
	}
}

func TestClassifyGeminiProtocolProbeIsNonDestructiveByDefault(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		err    error
		want   ProtocolProbeVerdict
	}{
		{name: "parseable success", status: http.StatusOK, body: `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`, want: ProtocolProbePositive},
		{name: "parseable safety response", status: http.StatusOK, body: `{"promptFeedback":{"blockReason":"SAFETY"}}`, want: ProtocolProbePositive},
		{name: "authentication", status: http.StatusUnauthorized, body: `{"error":{"status":"UNAUTHENTICATED"}}`, want: ProtocolProbeInconclusive},
		{name: "rate limit", status: http.StatusTooManyRequests, body: `{"error":{"status":"RESOURCE_EXHAUSTED"}}`, want: ProtocolProbeInconclusive},
		{name: "server error", status: http.StatusServiceUnavailable, body: `{"error":{"status":"UNAVAILABLE"}}`, want: ProtocolProbeInconclusive},
		{name: "raw method not allowed", status: http.StatusMethodNotAllowed, body: `method not allowed`, want: ProtocolProbeInconclusive},
		{name: "raw not found", status: http.StatusNotFound, body: `not found`, want: ProtocolProbeInconclusive},
		{name: "authentication reason cannot remove capability", status: http.StatusUnauthorized, body: `{"error":{"details":[{"reason":"METHOD_NOT_SUPPORTED"}]}}`, want: ProtocolProbeInconclusive},
		{name: "bad request reason cannot remove capability", status: http.StatusBadRequest, body: `{"error":{"details":[{"reason":"METHOD_NOT_SUPPORTED"}]}}`, want: ProtocolProbeInconclusive},
		{name: "model missing", status: http.StatusNotFound, body: `{"error":{"status":"NOT_FOUND","message":"model gemini-x was not found"}}`, want: ProtocolProbeModelSpecific},
		{name: "explicit provider method unsupported", status: http.StatusNotFound, body: `{"error":{"status":"NOT_FOUND","details":[{"reason":"METHOD_NOT_SUPPORTED"}]}}`, want: ProtocolProbeEndpointNegative},
		{name: "explicit provider method not allowed", status: http.StatusMethodNotAllowed, body: `{"error":{"details":[{"reason":"UNSUPPORTED_METHOD"}]}}`, want: ProtocolProbeEndpointNegative},
		{name: "2xx error envelope is not a Gemini response", status: http.StatusOK, body: `{"error":{"status":"UNAVAILABLE"}}`, want: ProtocolProbeInconclusive},
		{name: "2xx unrelated json is not a Gemini response", status: http.StatusOK, body: `{"ok":true}`, want: ProtocolProbeInconclusive},
		{name: "malformed success", status: http.StatusOK, body: `not-json`, want: ProtocolProbeInconclusive},
		{name: "network", err: io.ErrUnexpectedEOF, want: ProtocolProbeInconclusive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyGeminiProtocolProbe(tt.status, []byte(tt.body), tt.err); got != tt.want {
				t.Fatalf("classifyGeminiProtocolProbe() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyAntigravityGeminiProtocolProbeRequiresParsedSuccess(t *testing.T) {
	tests := []struct {
		name   string
		result *TestConnectionResult
		err    error
		want   ProtocolProbeVerdict
	}{
		{
			name: "parseable success",
			result: &TestConnectionResult{
				StatusCode:   http.StatusOK,
				ResponseBody: []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}}`),
			},
			want: ProtocolProbePositive,
		},
		{
			name: "malformed success",
			result: &TestConnectionResult{
				StatusCode:   http.StatusOK,
				ResponseBody: []byte(`not-json`),
				Text:         "must not substitute for wire parseability",
			},
			want: ProtocolProbeInconclusive,
		},
		{name: "missing result", want: ProtocolProbeInconclusive},
		{
			name: "explicit unsupported method",
			result: &TestConnectionResult{
				StatusCode:   http.StatusMethodNotAllowed,
				ResponseBody: []byte(`{"error":{"details":[{"reason":"METHOD_NOT_SUPPORTED"}]}}`),
			},
			err:  errors.New("upstream rejected request"),
			want: ProtocolProbeEndpointNegative,
		},
		{
			name: "authentication remains inconclusive",
			result: &TestConnectionResult{
				StatusCode:   http.StatusUnauthorized,
				ResponseBody: []byte(`{"error":{"details":[{"reason":"METHOD_NOT_SUPPORTED"}]}}`),
			},
			err:  errors.New("upstream rejected request"),
			want: ProtocolProbeInconclusive,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyAntigravityGeminiProtocolProbe(tt.result, tt.err); got != tt.want {
				t.Fatalf("classifyAntigravityGeminiProtocolProbe() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProbeAccountProtocolCapabilitiesSupportsGrokOAuth(t *testing.T) {
	account := &Account{
		ID:          198,
		Name:        "grok-oauth",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"access_token": "grok-oauth-secret",
			"base_url":     "https://grok.example.test/v1",
		},
		UpdatedAt: time.Date(2026, 8, 25, 6, 0, 0, 0, time.UTC),
	}
	repo := &protocolProbeCASRepo{account: cloneProtocolProbeAccount(account)}
	upstream := &protocolProbeSetUpstream{}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled: false,
		}}},
	}

	result, err := svc.ProbeAccountProtocolCapabilitiesNow(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("ProbeAccountProtocolCapabilitiesNow: %v", err)
	}
	if result.Outcome != ProtocolProbeRunUpdated {
		t.Fatalf("probe outcome = %q, want %q", result.Outcome, ProtocolProbeRunUpdated)
	}

	got, err := repo.GetByID(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if want := []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses}; !reflect.DeepEqual(got.SupportedProtocols(), want) {
		t.Fatalf("supported protocols = %v, want %v", got.SupportedProtocols(), want)
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	wantPaths := []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"}
	gotPaths := append([]string(nil), upstream.paths...)
	slices.Sort(gotPaths)
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("probe paths = %v, want %v", gotPaths, wantPaths)
	}
	for _, authorization := range upstream.authorizations {
		if authorization != "Bearer grok-oauth-secret" {
			t.Fatalf("probe authorization = %q, want bearer Grok access token", authorization)
		}
	}
	for _, profile := range upstream.profiles {
		if profile != HTTPUpstreamProfileGrok {
			t.Fatalf("probe HTTP profile = %q, want %q", profile, HTTPUpstreamProfileGrok)
		}
	}
	for _, redirectsOff := range upstream.redirectsOff {
		if !redirectsOff {
			t.Fatal("credential-bearing Grok OAuth probe allowed HTTP redirects")
		}
	}
	for _, version := range upstream.grokVersions {
		if strings.TrimSpace(version) == "" {
			t.Fatal("Grok OAuth probe omitted the pinned CLI identity headers")
		}
	}
	for i := range upstream.originators {
		if upstream.originators[i] != "" || upstream.codexWindows[i] != "" {
			t.Fatalf(
				"Grok OAuth probe leaked Codex identity headers: originator=%q window=%q",
				upstream.originators[i],
				upstream.codexWindows[i],
			)
		}
	}
}

func TestProbeAccountProtocolCapabilitiesSupportsCustomAnthropicOAuth(t *testing.T) {
	account := &Account{
		ID:          97,
		Name:        "custom-anthropic-oauth",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"access_token": "oauth-secret"},
		Extra: map[string]any{
			"custom_base_url_enabled": true,
			"custom_base_url":         "https://oauth-relay.example.test",
		},
		UpdatedAt: time.Date(2026, 8, 25, 5, 0, 0, 0, time.UTC),
	}
	repo := &protocolProbeCASRepo{account: cloneProtocolProbeAccount(account)}
	upstream := &protocolProbeSetUpstream{}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled: false,
		}}},
	}

	svc.ProbeAccountProtocolCapabilities(context.Background(), account.ID)

	got, err := repo.GetByID(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if want := []protocolrouter.Protocol{protocolrouter.ProtocolMessages}; !reflect.DeepEqual(got.SupportedProtocols(), want) {
		t.Fatalf("supported protocols = %v, want %v", got.SupportedProtocols(), want)
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if want := []string{"/v1/messages"}; !reflect.DeepEqual(upstream.paths, want) {
		t.Fatalf("probe paths = %v, want %v", upstream.paths, want)
	}
	if want := []string{"Bearer oauth-secret"}; !reflect.DeepEqual(upstream.authorizations, want) {
		t.Fatalf("probe authorizations = %v, want %v", upstream.authorizations, want)
	}
}

func TestProbeAccountProtocolCapabilitiesEvaluatesCandidateSetAndPersistsOnce(t *testing.T) {
	account := protocolRoutingOpenAIAccount(90)
	account.UpdatedAt = time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	repo := &protocolProbeCASRepo{account: cloneProtocolProbeAccount(account)}
	upstream := &protocolProbeSetUpstream{}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled: false,
		}}},
	}

	svc.ProbeAccountProtocolCapabilities(context.Background(), account.ID)

	got, err := repo.GetByID(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if want := []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses}; !reflect.DeepEqual(got.SupportedProtocols(), want) {
		t.Fatalf("supported protocols = %v, want %v", got.SupportedProtocols(), want)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("complete-set persistence calls = %d, want 1", repo.updateCalls)
	}
	gotPaths := append([]string(nil), upstream.paths...)
	wantPaths := []string{"/v1/messages", "/v1/chat/completions", "/v1/responses"}
	slices.Sort(gotPaths)
	slices.Sort(wantPaths)
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("probe paths = %v, want %v", gotPaths, wantPaths)
	}
}

func TestProbeAccountProtocolCapabilitiesProbesCandidateSetConcurrently(t *testing.T) {
	account := protocolRoutingOpenAIAccount(91)
	account.UpdatedAt = time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	repo := &protocolProbeCASRepo{account: cloneProtocolProbeAccount(account)}
	upstream := &protocolProbeBarrierUpstream{
		allStarted:  make(chan struct{}),
		release:     make(chan struct{}),
		wantStarted: 3,
	}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled: false,
		}}},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.ProbeAccountProtocolCapabilities(context.Background(), account.ID)
	}()

	select {
	case <-upstream.allStarted:
		close(upstream.release)
	case <-time.After(5 * time.Second):
		close(upstream.release)
		<-done
		t.Fatal("candidate protocol probes did not overlap within one account job")
	}
	<-done

	upstream.mu.Lock()
	maxInFlight := upstream.maxInFlight
	upstream.mu.Unlock()
	if maxInFlight != upstream.wantStarted {
		t.Fatalf("max concurrent protocol probes = %d, want %d", maxInFlight, upstream.wantStarted)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("complete-set persistence calls = %d, want 1", repo.updateCalls)
	}
}

func TestProtocolProbeCoordinatorCoalescesByEndpointCapabilityKey(t *testing.T) {
	var coordinator protocolProbeCoordinator
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	job := func() (ProtocolProbeRunResult, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return ProtocolProbeRunResult{Outcome: ProtocolProbeRunUnchanged}, nil
	}

	errCh := make(chan error, 2)
	go func() { _, err := coordinator.Do("capability-a", job); errCh <- err }()
	<-started
	go func() { _, err := coordinator.Do("capability-a", job); errCh <- err }()
	time.Sleep(20 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("coalesced probe: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("identical probe executions = %d, want 1", got)
	}

	started = make(chan struct{})
	release = make(chan struct{})
	calls.Store(0)
	job = func() (ProtocolProbeRunResult, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return ProtocolProbeRunResult{Outcome: ProtocolProbeRunUnchanged}, nil
	}
	errCh = make(chan error, 2)
	go func() {
		_, err := coordinator.Do("capability-a", job)
		errCh <- err
	}()
	<-started
	go func() {
		_, err := coordinator.Do("capability-b", job)
		errCh <- err
	}()
	deadline := time.After(time.Second)
	for calls.Load() != 2 {
		select {
		case <-deadline:
			t.Fatalf("different endpoint keys were coalesced; calls=%d", calls.Load())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(release)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("distinct probe: %v", err)
		}
	}
}

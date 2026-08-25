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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

type protocolProbeCASRepo struct {
	AccountRepository
	mu              sync.Mutex
	account         *Account
	waitForFirstTwo bool
	casArrivals     int
	casReady        chan struct{}
	updateCalls     int
}

func (r *protocolProbeCASRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	return cloneProtocolProbeAccount(r.account), nil
}

func (r *protocolProbeCASRepo) UpdateExtraIfUpdatedAt(
	_ context.Context,
	id int64,
	expectedUpdatedAt time.Time,
	updates map[string]any,
) (bool, error) {
	r.mu.Lock()
	if r.account == nil || r.account.ID != id {
		r.mu.Unlock()
		return false, ErrAccountNotFound
	}
	if r.waitForFirstTwo && r.casArrivals < 2 {
		r.casArrivals++
		if r.casArrivals == 2 {
			close(r.casReady)
		}
		r.mu.Unlock()
		<-r.casReady
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	if !r.account.UpdatedAt.Equal(expectedUpdatedAt) {
		return false, nil
	}
	if r.account.Extra == nil {
		r.account.Extra = make(map[string]any)
	}
	for key, value := range updates {
		r.account.Extra[key] = value
	}
	r.updateCalls++
	r.account.UpdatedAt = r.account.UpdatedAt.Add(time.Nanosecond)
	return true, nil
}

type protocolProbeSetUpstream struct {
	mu             sync.Mutex
	paths          []string
	authorizations []string
}

type protocolProbeBarrierUpstream struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	started     int
	allStarted  chan struct{}
	release     chan struct{}
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
	if u.started == len(protocolrouter.AllProtocols()) {
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
			want: protocolrouter.AllProtocols(),
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
			name: "ungoverned gemini is excluded",
			account: &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "secret", "base_url": "https://gemini.example.test",
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

func TestProbeAccountProtocolCapabilitiesSupportsCustomAnthropicOAuth(t *testing.T) {
	account := &Account{
		ID:          97,
		Name:        "custom-anthropic-oauth",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
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

func TestBuildProtocolProbeUpdateRejectsStaleAccountRevision(t *testing.T) {
	account := protocolRoutingOpenAIAccount(55, "responses")
	expected, err := protocolProbeConfigurationRevision(account)
	if err != nil {
		t.Fatalf("protocolProbeConfigurationRevision: %v", err)
	}
	account.Credentials["base_url"] = "https://changed.example.test/v1"

	_, err = BuildProtocolProbeUpdate(account, expected, map[protocolrouter.Protocol]ProtocolProbeVerdict{
		protocolrouter.ProtocolChatCompletions: ProtocolProbePositive,
	})
	if !errors.Is(err, ErrProtocolProbeStaleRevision) {
		t.Fatalf("BuildProtocolProbeUpdate error = %v, want ErrProtocolProbeStaleRevision", err)
	}
}

func TestProtocolProbeFactsRemainPerAccountEvenWhenBaseURLMatches(t *testing.T) {
	first := protocolRoutingOpenAIAccount(1, "responses")
	second := protocolRoutingOpenAIAccount(2, "messages")
	first.Credentials["base_url"] = "https://shared.example.test/v1"
	second.Credentials["base_url"] = "https://shared.example.test/v1"

	firstRevision, _ := protocolProbeConfigurationRevision(first)
	secondRevision, _ := protocolProbeConfigurationRevision(second)
	firstUpdate, err := BuildProtocolProbeUpdate(first, firstRevision, map[protocolrouter.Protocol]ProtocolProbeVerdict{
		protocolrouter.ProtocolResponses: ProtocolProbeEndpointNegative,
	})
	if err != nil {
		t.Fatalf("BuildProtocolProbeUpdate first: %v", err)
	}
	secondUpdate, err := BuildProtocolProbeUpdate(second, secondRevision, map[protocolrouter.Protocol]ProtocolProbeVerdict{
		protocolrouter.ProtocolMessages: ProtocolProbePositive,
	})
	if err != nil {
		t.Fatalf("BuildProtocolProbeUpdate second: %v", err)
	}
	if reflect.DeepEqual(firstUpdate[SupportedProtocolsExtraKey], secondUpdate[SupportedProtocolsExtraKey]) {
		t.Fatalf("shared base URL collapsed per-account capability facts: first=%v second=%v", firstUpdate, secondUpdate)
	}
}

func TestPersistProtocolProbeVerdictsMergesConcurrentSiblingResultsWithoutLoss(t *testing.T) {
	account := protocolRoutingOpenAIAccount(88)
	account.UpdatedAt = time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	revision, err := protocolProbeConfigurationRevision(account)
	if err != nil {
		t.Fatalf("protocolProbeConfigurationRevision: %v", err)
	}
	repo := &protocolProbeCASRepo{
		account:         cloneProtocolProbeAccount(account),
		waitForFirstTwo: true,
		casReady:        make(chan struct{}),
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- PersistProtocolProbeVerdicts(
			context.Background(), repo, account.ID, revision,
			map[protocolrouter.Protocol]ProtocolProbeVerdict{protocolrouter.ProtocolMessages: ProtocolProbePositive},
			map[string]any{"openai_native_messages_supported": true},
		)
	}()
	go func() {
		errCh <- PersistProtocolProbeVerdicts(
			context.Background(), repo, account.ID, revision,
			map[protocolrouter.Protocol]ProtocolProbeVerdict{protocolrouter.ProtocolResponses: ProtocolProbePositive},
			map[string]any{"openai_responses_supported": true},
		)
	}()
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("PersistProtocolProbeVerdicts: %v", err)
		}
	}

	got, err := repo.GetByID(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	want := []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses}
	if !reflect.DeepEqual(got.SupportedProtocols(), want) {
		t.Fatalf("supported protocols = %v, want %v", got.SupportedProtocols(), want)
	}
	if got.Extra["openai_native_messages_supported"] != true || got.Extra["openai_responses_supported"] != true {
		t.Fatalf("legacy rollback facts were not preserved: extra=%v", got.Extra)
	}
}

func TestPersistProtocolProbeVerdictsRejectsConfigurationChangedAfterProbe(t *testing.T) {
	account := protocolRoutingOpenAIAccount(89, "responses")
	account.UpdatedAt = time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	revision, err := protocolProbeConfigurationRevision(account)
	if err != nil {
		t.Fatalf("protocolProbeConfigurationRevision: %v", err)
	}
	repo := &protocolProbeCASRepo{account: cloneProtocolProbeAccount(account)}
	repo.account.Credentials["base_url"] = "https://changed.example.test/v1"
	repo.account.UpdatedAt = repo.account.UpdatedAt.Add(time.Second)

	err = PersistProtocolProbeVerdicts(
		context.Background(), repo, account.ID, revision,
		map[protocolrouter.Protocol]ProtocolProbeVerdict{protocolrouter.ProtocolMessages: ProtocolProbePositive},
		nil,
	)
	if !errors.Is(err, ErrProtocolProbeStaleRevision) {
		t.Fatalf("PersistProtocolProbeVerdicts error = %v, want ErrProtocolProbeStaleRevision", err)
	}
	if got := repo.account.SupportedProtocols(); !reflect.DeepEqual(got, []protocolrouter.Protocol{protocolrouter.ProtocolResponses}) {
		t.Fatalf("stale probe changed supported protocols: %v", got)
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
	if want := protocolrouter.AllProtocols(); !reflect.DeepEqual(got.SupportedProtocols(), want) {
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
		allStarted: make(chan struct{}),
		release:    make(chan struct{}),
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
	if maxInFlight != len(protocolrouter.AllProtocols()) {
		t.Fatalf("max concurrent protocol probes = %d, want %d", maxInFlight, len(protocolrouter.AllProtocols()))
	}
	if repo.updateCalls != 1 {
		t.Fatalf("complete-set persistence calls = %d, want 1", repo.updateCalls)
	}
}

func TestProtocolProbeCoordinatorCoalescesOnlyIdenticalAccountRevisionCandidateSet(t *testing.T) {
	var coordinator protocolProbeCoordinator
	allCandidates := protocolrouter.AllProtocols()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	job := func() error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}

	errCh := make(chan error, 2)
	go func() { errCh <- coordinator.Do(7, "revision-a", allCandidates, job) }()
	<-started
	go func() { errCh <- coordinator.Do(7, "revision-a", allCandidates, job) }()
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
	job = func() error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}
	errCh = make(chan error, 2)
	go func() {
		errCh <- coordinator.Do(7, "revision-a", []protocolrouter.Protocol{protocolrouter.ProtocolMessages}, job)
	}()
	<-started
	go func() {
		errCh <- coordinator.Do(7, "revision-a", []protocolrouter.Protocol{protocolrouter.ProtocolResponses}, job)
	}()
	deadline := time.After(time.Second)
	for calls.Load() != 2 {
		select {
		case <-deadline:
			t.Fatalf("different candidate-set jobs were coalesced; calls=%d", calls.Load())
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

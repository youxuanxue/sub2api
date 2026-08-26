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

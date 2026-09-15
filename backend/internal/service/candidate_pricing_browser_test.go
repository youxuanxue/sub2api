//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

// The browser consumes the real catalog/candidate service over HTTP. Only
// storage and authentication inputs are fixtures; no catalog response is mocked.
func TestCandidatePricingBrowser(t *testing.T) {
	if os.Getenv("TK_CANDIDATE_BROWSER") != "1" {
		t.Skip("run with TK_CANDIDATE_BROWSER=1 after pnpm install and playwright install chromium")
	}
	groups := []Group{grp(11, PlatformAnthropic, 0, false), grp(12, PlatformOpenAI, 1, false)}
	groups[0].Name, groups[1].Name = "Cross-platform billing", "Universal peer"
	models := []string{"ssot-direct", "ssot-alias", "ssot-universal", "openai/gpt-5.4", "openai/gpt-5.6"}
	sharedModel := "ssot-platform-peer"
	accounts := []Account{globalCandidateAccount(1, 1, 11), globalCandidateAccount(2, 1, 12), globalCandidateAccount(3, 1, 12)}
	accounts[0].Credentials["model_mapping"] = map[string]any{models[0]: "gpt-5.4", models[1]: "gpt-5.4", models[3]: "gpt-5.4", models[4]: "gpt-5.6-sol"}
	accounts[0].Credentials["model_mapping"].(map[string]any)[sharedModel] = "gpt-5.4"
	accounts[1].Credentials["model_mapping"] = map[string]any{models[2]: "gpt-5.4"}
	accounts[2].Platform, accounts[2].ChannelType = PlatformNewAPI, 1
	accounts[2].Credentials["model_mapping"] = map[string]any{sharedModel: "gpt-5.4"}
	attachTestProtocolCapability(&accounts[2], protocolrouter.ProtocolChatCompletions)
	for i := range groups {
		pricedModels := append(append([]string{}, models[:3]...), sharedModel)
		groups[i].ModelPricing = []ChannelModelPricing{{Platform: PlatformOpenAI, Models: pricedModels, InputPrice: ptrF(.001), OutputPrice: ptrF(.002)}}
	}
	capabilities, key := candidateDiscoveryFixture(groups, accounts)
	capabilities.resolver.candidateGateway.billingService = NewBillingService(nil, nil)
	direct := *key
	direct.ID, direct.Name, direct.RoutingMode, direct.GroupID, direct.Group = 42, "Direct", RoutingModeDirect, &groups[0].ID, &groups[0]
	key.ID, key.Name = 43, "Universal"
	public := *NewPricingCatalogService(nil).BuildPublicCatalog(context.Background())
	public.Data = append([]PublicCatalogModel(nil), public.Data...)
	for _, model := range models[:3] {
		public.Data = append(public.Data, mkPublicCatalogModel(model, "openai", 1, 2, 0))
	}
	public.Data = append(public.Data, mkPublicCatalogModel(sharedModel, "openai", 1, 2, 0))
	// Construct per request: no concurrent mutation of fake repository objects.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.URL.Path == "/fixture" {
			_ = json.NewEncoder(w).Encode(map[string]any{"models": models, "shared_model": sharedModel, "user_id": key.UserID})
			return
		}
		if req.URL.Path != "/api/v1/me/pricing-catalog" {
			http.NotFound(w, req)
			return
		}
		svc := newServiceWithAccounts(&fakeKeyAccess{groups: groups, keys: []APIKey{direct, *key}},
			&fakeChannelLister{}, &fakeCatalogProvider{resp: &public}, &fakeAccountSource{})
		kind := req.Header.Get("X-Test-Failure-Kind")
		repo := &batchAvailabilityRepoStub{states: map[string]AvailabilityState{}}
		observedAt := time.Now()
		if req.Header.Get("X-Test-Evidence-Expired") == "true" {
			observedAt = observedAt.Add(-7 * 24 * time.Hour)
		}
		for _, model := range append(append([]string{}, models...), sharedModel) {
			repo.states[PlatformOpenAI+"/"+model] = AvailabilityState{Status: AvailabilityStatusUnreachable, LastFailureKind: kind, LastFailureAt: &observedAt}
		}
		availability := NewPricingAvailabilityService(repo, time.Now)
		requestCapabilities := *capabilities
		requestCapabilities.modelFilter = NewModelListFilter(NewPricingCatalogService(nil), availability)
		svc.capabilities = &requestCapabilities
		svc.availability = availability
		opts := MePricingCatalogOptions{HideUserRateOverrides: true}
		for name, target := range map[string]**int64{"api_key_id": &opts.APIKeyID, "group_id": &opts.GroupID} {
			if raw := req.URL.Query().Get(name); raw != "" {
				id, err := strconv.ParseInt(raw, 10, 64)
				if err != nil {
					http.Error(w, "invalid scope", http.StatusBadRequest)
					return
				}
				*target = &id
			}
		}
		result, err := svc.BuildForUser(req.Context(), key.UserID, opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success", "data": result})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pnpm", "exec", "playwright", "test", "--config", "playwright.candidate.config.ts")
	cmd.Dir = filepath.Join("..", "..", "..", "frontend")
	cmd.Env = append(os.Environ(), "TK_CANDIDATE_BACKEND_URL="+server.URL)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	t.Log(string(output))
}

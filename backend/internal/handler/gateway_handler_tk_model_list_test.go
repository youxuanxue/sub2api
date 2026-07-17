//go:build unit

package handler

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Tests for gateway_handler_tk_model_list.go helpers.
//
// These pin the shape/scope fixes from review-20260507 R-001 (AntigravityModels
// shape regression) and R-002 (cross-platform scope regression — candidate set
// must be antigravity.DefaultModels(), not the full pricing catalog)
// and CF-001 (GeminiV1BetaListModels fallback filter).

// --- tkAntigravityDefaultModels ---

func TestTkAntigravityDefaultModels_ReturnsClaudeModelShape(t *testing.T) {
	// Regression pin for R-001: output must be []antigravity.ClaudeModel,
	// not []string. Any future change that returns bare strings will break
	// this compilation + assertion.
	h := &GatewayHandler{}
	result := h.tkAntigravityDefaultModels(context.Background())

	// The type assertion is implicit via the function signature returning
	// []antigravity.ClaudeModel; we additionally verify struct fields are set.
	require.NotEmpty(t, result, "should return default antigravity models when filter not wired")
	for _, m := range result {
		require.NotEmpty(t, m.ID, "each model must have a non-empty ID")
		require.Equal(t, "model", m.Type, "Type field must be 'model'")
	}
}

func TestTkAntigravityDefaultModels_ScopeIsAntigravityOnly(t *testing.T) {
	// Regression pin for R-002 updated by the 2026-06-23 Antigravity refresh:
	// the fallback source is the empirically servable Antigravity allowlist, not
	// the whole pricing catalog and not the raw DefaultModels advertisement.
	//
	// Wire a filter that returns everything as priced, then verify output is
	// still scoped to the Antigravity servable candidate set.
	repo := &capturedRepo2{rows: map[string]service.AvailabilityState{}}
	availSvc := service.NewPricingAvailabilityService(repo, time.Now)
	filter := service.NewModelListFilter(nil, availSvc) // pricing nil → fail-open (all pass)
	h := &GatewayHandler{tkModelListFilter: filter}

	result := h.tkAntigravityDefaultModels(context.Background())
	allow := service.ServableClientFacingIDs(context.Background(), service.PlatformAntigravity, nil, nil)

	// Every returned model must come from the antigravity servable set.
	allowIDs := make(map[string]bool, len(allow))
	for _, id := range allow {
		allowIDs[id] = true
	}
	for _, m := range result {
		require.True(t, allowIDs[m.ID],
			"output model %q is not in the Antigravity servable allowlist — cross-platform leakage", m.ID)
	}
}

func TestTkAntigravityDefaultModels_FilterDropsUnreachable(t *testing.T) {
	ctx := context.Background()
	repo := &capturedRepo2{rows: map[string]service.AvailabilityState{}}
	availSvc := service.NewPricingAvailabilityService(repo, time.Now)
	ownerIDs := service.ServableClientFacingIDs(ctx, service.PlatformAntigravity, nil, nil)
	targetID, survivorID := firstTwoIDsForHandlerTest(t, ownerIDs)

	baseline := modelIDsFromAntigravityModels((&GatewayHandler{}).tkAntigravityDefaultModels(ctx))
	require.Contains(t, baseline, targetID, "SSOT-derived prune target must exist before availability changes")
	require.Contains(t, baseline, survivorID, "SSOT-derived survivor must exist before availability changes")

	// Drive target model to unreachable
	availSvc.RecordOutcome(ctx, service.AvailabilityOutcome{
		Platform:           service.PlatformAntigravity,
		ModelID:            targetID,
		Success:            false,
		UpstreamStatusCode: 404,
		UpstreamErrorBody:  `{"error":{"message":"Requested entity was not found."}}`,
	})

	// FilterClientFacing requires a non-nil pricing service (pricing=nil → fail-open, skip availability check too).
	// Use a PricingCatalogService with all antigravity models priced so the availability filter runs.
	pricingSvc := buildTestPricingService(t, buildPricingJSONFromIDs(ownerIDs))

	filter := service.NewModelListFilter(pricingSvc, availSvc)
	h := &GatewayHandler{tkModelListFilter: filter}

	resultIDs := modelIDsFromAntigravityModels(h.tkAntigravityDefaultModels(ctx))
	require.NotContains(t, resultIDs, targetID, "unreachable model must not appear in output")
	require.Contains(t, resultIDs, survivorID, "an unaffected SSOT sibling must remain in output")
}

func TestTkAntigravityDefaultModels_NilFilterIsFailOpen(t *testing.T) {
	// Post-SSOT convergence: nil filter still uses the unified servable candidate
	// set, so SDKs see the current Antigravity allowlist without requiring pricing
	// wiring. It does not fall back to raw DefaultModels, which still contains
	// claude/gpt-oss and unprobed Gemini ids.
	h := &GatewayHandler{}
	result := h.tkAntigravityDefaultModels(context.Background())
	require.NotEmpty(t, result, "nil filter must still produce a non-empty list")
	for _, m := range result {
		require.Equal(t, "model", m.Type, "synthesized allowlist-only entries must keep the Claude model shape")
	}
	require.ElementsMatch(t,
		service.ServableClientFacingIDs(context.Background(), service.PlatformAntigravity, nil, nil),
		modelIDsFromAntigravityModels(result),
		"nil filter must still mirror the unified Antigravity SSOT")
}

func TestTkAntigravityDefaultModels_PricedServableSetIncludesReprobedGeminiIDs(t *testing.T) {
	ctx := context.Background()
	allow := service.ServableClientFacingIDs(ctx, service.PlatformAntigravity, nil, nil)
	allowSet := stringBoolSetForHandlerTest(allow)
	// Price every Antigravity SSOT id, plus a Gemini-only candidate, to prove the
	// filter is controlled by the Antigravity owner instead of the pricing owner.
	geminiOnly := firstIDOutsideSetForHandlerTest(t,
		service.ServableClientFacingIDs(ctx, service.PlatformGemini, nil, nil), allowSet)
	pricingIDs := append(append([]string{}, allow...), geminiOnly)
	pricingSvc := buildTestPricingService(t, buildPricingJSONFromIDs(pricingIDs))
	filter := service.NewModelListFilter(pricingSvc, nil)
	h := &GatewayHandler{tkModelListFilter: filter}

	result := h.tkAntigravityDefaultModels(ctx)
	ids := make(map[string]bool, len(result))
	for _, m := range result {
		ids[m.ID] = true
		require.Equal(t, "model", m.Type, "all returned models must keep the Claude model-list shape")
	}
	require.ElementsMatch(t,
		service.ServableClientFacingIDs(ctx, service.PlatformAntigravity, nil, pricingSvc),
		modelIDsFromAntigravityModels(result),
		"/antigravity/models must mirror the unified priced+servable SSOT")
	require.False(t, ids[geminiOnly], "%s must not leak into /antigravity/models", geminiOnly)
	require.False(t, ids["gpt-oss-120b-medium"], "unsupported gpt-oss boundary sample must not leak into /antigravity/models")
}

func TestTkOpenAIDefaultModelIDs_DropsAdvertisedDead(t *testing.T) {
	h := &GatewayHandler{}
	result := h.tkOpenAIDefaultModelIDs(context.Background(), service.PlatformOpenAI)
	require.NotEmpty(t, result)

	require.ElementsMatch(t,
		service.ServableClientFacingIDs(context.Background(), service.PlatformOpenAI, nil, nil),
		modelIDsFromOpenAIModels(result),
		"OpenAI default model list must mirror the unified servable SSOT")
}

// --- tkGeminiFallbackModelsList ---

func TestTkGeminiFallbackModelsList_ReturnsModelsListResponse(t *testing.T) {
	h := &GatewayHandler{}
	result := h.tkGeminiFallbackModelsList(context.Background())

	require.IsType(t, gemini.ModelsListResponse{}, result)
	require.NotEmpty(t, result.Models, "should return default Gemini models when filter not wired")
	for _, m := range result.Models {
		require.Contains(t, m.Name, "models/", "Gemini model Name must have 'models/' prefix")
	}
}

func TestTkGeminiFallbackModelsList_NilFilterIsFailOpen(t *testing.T) {
	// Post-SSOT-convergence (Goal 1): the nil-filter fail-open returns the unified
	// servable candidate set (the empirical gemini allowlist), NOT the raw canonical
	// gemini.DefaultModels(). It must stay non-empty (never break an SDK) AND drop
	// advertised_dead ids (gemini-2.0-flash) that the canonical list still carried.
	h := &GatewayHandler{}
	result := h.tkGeminiFallbackModelsList(context.Background())
	require.NotEmpty(t, result.Models, "nil filter must still produce a non-empty list")
	for _, m := range result.Models {
		require.Contains(t, m.Name, "models/", "Gemini model Name must keep 'models/' prefix")
	}
	require.ElementsMatch(t,
		withGeminiModelPrefixForTest(service.ServableClientFacingIDs(context.Background(), service.PlatformGemini, nil, nil)),
		modelNamesFromGeminiModels(result.Models),
		"nil filter must mirror the unified Gemini SSOT, not raw advertised defaults")
}

func TestTkGeminiFallbackModelsList_FilterDropsUnreachable(t *testing.T) {
	repo := &capturedRepo2{rows: map[string]service.AvailabilityState{}}
	availSvc := service.NewPricingAvailabilityService(repo, time.Now)

	ctx := context.Background()
	servableGemini := service.ServableClientFacingIDs(ctx, service.PlatformGemini, nil, nil)
	targetID, survivorID := firstTwoIDsForHandlerTest(t, servableGemini)
	baseline := modelNamesFromGeminiModels((&GatewayHandler{}).tkGeminiFallbackModelsList(ctx).Models)
	require.Contains(t, baseline, "models/"+targetID, "SSOT-derived prune target must exist before availability changes")
	require.Contains(t, baseline, "models/"+survivorID, "SSOT-derived survivor must exist before availability changes")

	availSvc.RecordOutcome(context.Background(), service.AvailabilityOutcome{
		Platform:           service.PlatformGemini,
		ModelID:            targetID,
		Success:            false,
		UpstreamStatusCode: 404,
		UpstreamErrorBody:  `{"error":{"message":"Requested entity was not found."}}`,
	})

	// Price the servable gemini candidates so ∩priced keeps them and the
	// structurally-gone prune is what removes the target.
	pricingSvc := buildTestPricingService(t, buildPricingJSONFromIDs(servableGemini))
	filter := service.NewModelListFilter(pricingSvc, availSvc)
	h := &GatewayHandler{tkModelListFilter: filter}

	result := h.tkGeminiFallbackModelsList(context.Background())
	resultNames := modelNamesFromGeminiModels(result.Models)
	require.NotContains(t, resultNames, "models/"+targetID,
		"structurally-gone model must not appear in fallback response")
	require.Contains(t, resultNames, "models/"+survivorID,
		"an unaffected SSOT sibling must remain in fallback response")
}

// buildPricingJSON builds a minimal LiteLLM-shaped pricing JSON string where
// all models in the given []antigravity.ClaudeModel are priced at $0.001/1k.
func buildPricingJSON(models []antigravity.ClaudeModel) string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return buildPricingJSONFromIDs(ids)
}

func modelIDsFromAntigravityModels(models []antigravity.ClaudeModel) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

func modelIDsFromOpenAIModels(models []openai.Model) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

func modelNamesFromGeminiModels(models []gemini.Model) []string {
	names := make([]string, len(models))
	for i, m := range models {
		names[i] = m.Name
	}
	return names
}

func withGeminiModelPrefixForTest(ids []string) []string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = "models/" + id
	}
	return names
}

func stringBoolSetForHandlerTest(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func firstIDOutsideSetForHandlerTest(t *testing.T, candidates []string, excluded map[string]bool) string {
	t.Helper()
	for _, id := range candidates {
		if !excluded[id] {
			return id
		}
	}
	require.FailNow(t, "expected at least one candidate outside excluded set")
	return ""
}

func firstTwoIDsForHandlerTest(t *testing.T, candidates []string) (string, string) {
	t.Helper()
	require.GreaterOrEqual(t, len(candidates), 2, "SSOT sample source must contain a target and survivor")
	sorted := append([]string{}, candidates...)
	sort.Strings(sorted)
	return sorted[0], sorted[1]
}

// buildPricingJSONFromIDs builds a pricing JSON where each provided model ID
// has a non-nil input+output cost (required for PricingCatalogService to include it).
func buildPricingJSONFromIDs(ids []string) string {
	entries := make([]string, len(ids))
	for i, id := range ids {
		entries[i] = fmt.Sprintf(`%q: {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "litellm_provider": "test"}`, id)
	}
	return "{" + strings.Join(entries, ",") + "}"
}

// buildTestPricingService creates a PricingCatalogService with the given JSON as its source.
func buildTestPricingService(t *testing.T, json string) *service.PricingCatalogService {
	t.Helper()
	svc := service.NewPricingCatalogService(nil)
	data := []byte(json)
	svc.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return data, time.Now(), true
	})
	return svc
}

// capturedRepo2 mirrors capturedRepo from gateway_handler_tk_forward_error_test.go
// to avoid cross-test-file symbol collision (both are in package handler).
type capturedRepo2 struct {
	mu   sync.Mutex
	rows map[string]service.AvailabilityState
}

func (r *capturedRepo2) key(p, m string) string { return p + "::" + m }
func (r *capturedRepo2) Upsert(_ context.Context, p, m string, fn func(service.AvailabilityState) service.AvailabilityState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.rows[r.key(p, m)]
	r.rows[r.key(p, m)] = fn(cur)
	return nil
}
func (r *capturedRepo2) Get(_ context.Context, p, m string) (service.AvailabilityState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rows[r.key(p, m)], nil
}

func TestAntigravityModelScope(t *testing.T) {
	cases := map[string]string{
		"claude-sonnet-4-6":      "claude",
		"claude-opus-4-8":        "claude",
		"gpt-oss-120b-medium":    "gpt_oss",
		"gemini-3.1-flash-image": "gemini_image",
		"gemini-2.5-flash-image": "gemini_image",
		"gemini-3-pro-image":     "gemini_image",
		"gemini-3-flash":         "gemini_text",
		"gemini-pro-agent":       "gemini_text",
		"gemini-2.5-pro":         "gemini_text",
		"tab_flash_lite_preview": "gemini_text",
	}
	for id, want := range cases {
		if got := antigravityModelScope(id); got != want {
			t.Fatalf("antigravityModelScope(%q)=%q want %q", id, got, want)
		}
	}
}

func TestTkAntigravityFilterModelsByGroupScopes(t *testing.T) {
	models := []antigravity.ClaudeModel{
		{ID: "claude-sonnet-4-6"},
		{ID: "claude-opus-4-8"},
		{ID: "gpt-oss-120b-medium"},
		{ID: "gemini-3-flash"},
		{ID: "gemini-pro-agent"},
		{ID: "gemini-3.1-flash-image"},
	}
	ids := func(ms []antigravity.ClaudeModel) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = m.ID
		}
		return out
	}

	// Group without claude/gpt_oss scopes: claude + gpt-oss dropped.
	got := tkAntigravityFilterModelsByGroupScopes([]string{"gemini_text", "gemini_image"}, models)
	want := []string{"gemini-3-flash", "gemini-pro-agent", "gemini-3.1-flash-image"}
	if strings.Join(ids(got), ",") != strings.Join(want, ",") {
		t.Fatalf("scope filter without claude/gpt_oss = %v, want %v", ids(got), want)
	}

	// gemini_text only: image dropped too.
	got = tkAntigravityFilterModelsByGroupScopes([]string{"gemini_text"}, models)
	want = []string{"gemini-3-flash", "gemini-pro-agent"}
	if strings.Join(ids(got), ",") != strings.Join(want, ",") {
		t.Fatalf("gemini_text scope filter = %v, want %v", ids(got), want)
	}

	// claude included: claude kept (but gpt-oss still dropped — not a scope value).
	got = tkAntigravityFilterModelsByGroupScopes([]string{"claude", "gemini_text", "gemini_image"}, models)
	if strings.Join(ids(got), ",") != "claude-sonnet-4-6,claude-opus-4-8,gemini-3-flash,gemini-pro-agent,gemini-3.1-flash-image" {
		t.Fatalf("claude+gemini scope filter unexpected: %v", ids(got))
	}

	// empty scopes = no restriction (back-compat).
	got = tkAntigravityFilterModelsByGroupScopes(nil, models)
	if len(got) != len(models) {
		t.Fatalf("empty scopes should be unrestricted, got %d of %d", len(got), len(models))
	}
}

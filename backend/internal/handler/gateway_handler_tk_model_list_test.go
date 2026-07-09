//go:build unit

package handler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
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
	repo := &capturedRepo2{rows: map[string]service.AvailabilityState{}}
	availSvc := service.NewPricingAvailabilityService(repo, time.Now)

	const targetID = "gemini-2.5-flash"

	// Drive target model to unreachable
	availSvc.RecordOutcome(context.Background(), service.AvailabilityOutcome{
		Platform:           service.PlatformAntigravity,
		ModelID:            targetID,
		Success:            false,
		UpstreamStatusCode: 404,
		UpstreamErrorBody:  `{"error":{"message":"Requested entity was not found."}}`,
	})

	// FilterClientFacing requires a non-nil pricing service (pricing=nil → fail-open, skip availability check too).
	// Use a PricingCatalogService with all antigravity models priced so the availability filter runs.
	pricingSvc := buildTestPricingService(t, buildPricingJSONFromIDs(service.ServableClientFacingIDs(context.Background(), service.PlatformAntigravity, nil, nil)))

	filter := service.NewModelListFilter(pricingSvc, availSvc)
	h := &GatewayHandler{tkModelListFilter: filter}

	result := h.tkAntigravityDefaultModels(context.Background())
	for _, m := range result {
		require.NotEqual(t, targetID, m.ID, "unreachable model must not appear in output")
	}
}

func TestTkAntigravityDefaultModels_NilFilterIsFailOpen(t *testing.T) {
	// Post-SSOT convergence: nil filter still uses the unified servable candidate
	// set, so SDKs see the current Antigravity allowlist without requiring pricing
	// wiring. It does not fall back to raw DefaultModels, which still contains
	// claude/gpt-oss and unprobed Gemini ids.
	h := &GatewayHandler{}
	result := h.tkAntigravityDefaultModels(context.Background())
	require.NotEmpty(t, result, "nil filter must still produce a non-empty list")
	ids := make(map[string]bool, len(result))
	for _, m := range result {
		ids[m.ID] = true
		require.Equal(t, "model", m.Type, "synthesized allowlist-only entries must keep the Claude model shape")
	}
	require.True(t, ids["gemini-2.5-flash"], "live-servable Antigravity Gemini id present")
	require.True(t, ids["gemini-2.5-flash-thinking"], "overlay-priced Antigravity thinking id present")
	require.True(t, ids["gemini-3-flash-agent"], "allowlist-only Antigravity wire id present")
	require.False(t, ids["gemini-2.5-pro"], "unprobed/inconclusive gemini-2.5-pro must not appear")
	require.False(t, ids["claude-fable-5"], "claude is routed off Antigravity")
}

func TestTkAntigravityDefaultModels_PricedServableSetIncludesReprobedGeminiIDs(t *testing.T) {
	// Price only the source-backed Antigravity ids; tk_pricing_overlay.json must
	// provide the wire-only ids such as gemini-2.5-flash-thinking and
	// gemini-3-flash-agent. gemini-2.5-pro is intentionally priced in normal
	// Gemini catalogs but NOT in the Antigravity allowlist after the 2026-06-23
	// reprobe, so it must remain hidden here.
	pricingSvc := buildTestPricingService(t, buildPricingJSONFromIDs([]string{
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-3-flash",
		"gemini-3.1-flash-image",
		"gemini-3.1-pro-low",
		"gemini-2.5-pro",
	}))
	filter := service.NewModelListFilter(pricingSvc, nil)
	h := &GatewayHandler{tkModelListFilter: filter}

	result := h.tkAntigravityDefaultModels(context.Background())
	ids := make(map[string]bool, len(result))
	for _, m := range result {
		ids[m.ID] = true
		require.Equal(t, "model", m.Type, "all returned models must keep the Claude model-list shape")
	}
	for _, want := range []string{
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-2.5-flash-thinking",
		"gemini-3-flash",
		"gemini-3-flash-agent",
		"gemini-3.1-flash-image", // served via antigravity pool (2026-06-27 image probe 200)
		"gemini-3.1-pro-low",
		"gemini-3.5-flash-extra-low",
		"gemini-3.5-flash-low",
		"gemini-pro-agent",
	} {
		require.True(t, ids[want], "%s should be visible in /antigravity/models after pricing closure", want)
	}
	for _, deny := range []string{"gemini-2.5-pro", "claude-fable-5", "gpt-oss-120b-medium"} {
		require.False(t, ids[deny], "%s must not leak into /antigravity/models", deny)
	}
}

func TestTkOpenAIDefaultModelIDs_DropsAdvertisedDead(t *testing.T) {
	h := &GatewayHandler{}
	result := h.tkOpenAIDefaultModelIDs(context.Background(), service.PlatformOpenAI)
	require.NotEmpty(t, result)

	ids := make(map[string]bool, len(result))
	for _, m := range result {
		ids[m.ID] = true
	}
	require.True(t, ids["gpt-5.4"], "servable OpenAI default should remain visible")
	require.True(t, ids["codex-auto-review"], "live-servable codex-auto-review should remain visible")
	for _, want := range []string{"gpt-5-codex", "gpt-5.2", "gpt-5.2-pro", "gpt-5.3", "gpt-5.3-codex", "gpt-5.4-mini", "gpt-5.5"} {
		require.True(t, ids[want], "servable OpenAI probe result %s should remain visible", want)
	}
	for _, dead := range []string{"gpt-5-pro", "gpt-5.3-codex-spark", "gpt-image-1", "gpt-image-1.5", "gpt-image-2"} {
		require.False(t, ids[dead], "advertised_dead %s must not reach /v1/models fallback", dead)
	}
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
	names := make(map[string]bool, len(result.Models))
	for _, m := range result.Models {
		require.Contains(t, m.Name, "models/", "Gemini model Name must keep 'models/' prefix")
		names[m.Name] = true
	}
	require.True(t, names["models/gemini-2.5-flash"], "servable gemini-2.5-flash present")
	require.False(t, names["models/gemini-2.0-flash"],
		"advertised_dead gemini-2.0-flash dropped — converged to the servable allowlist")
}

func TestTkGeminiFallbackModelsList_FilterDropsUnreachable(t *testing.T) {
	repo := &capturedRepo2{rows: map[string]service.AvailabilityState{}}
	availSvc := service.NewPricingAvailabilityService(repo, time.Now)

	// Target an id that IS in the servable allowlist, so the structurally-gone
	// prune is genuinely exercised (gemini-2.0-flash is no longer a candidate).
	const targetID = "gemini-2.5-flash"
	availSvc.RecordOutcome(context.Background(), service.AvailabilityOutcome{
		Platform:           service.PlatformGemini,
		ModelID:            targetID,
		Success:            false,
		UpstreamStatusCode: 404,
		UpstreamErrorBody:  `{"error":{"message":"Requested entity was not found."}}`,
	})

	// Price the servable gemini chat candidates so ∩priced keeps them and the
	// structurally-gone prune is what removes the target.
	pricingSvc := buildTestPricingService(t, buildPricingJSONFromIDs([]string{
		"gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-2.5-pro",
	}))
	filter := service.NewModelListFilter(pricingSvc, availSvc)
	h := &GatewayHandler{tkModelListFilter: filter}

	result := h.tkGeminiFallbackModelsList(context.Background())
	for _, m := range result.Models {
		require.NotEqual(t, "models/"+targetID, m.Name,
			"structurally-gone gemini-2.5-flash must not appear in fallback response")
	}
	require.True(t, len(result.Models) > 0, "reachable+priced siblings (e.g. gemini-2.5-pro) must remain")
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

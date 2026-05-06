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
	// Regression pin for R-002: models that are NOT in antigravity.DefaultModels()
	// must never appear in the output, even if the pricing catalog contains them.
	//
	// Wire a filter that returns everything as priced, then verify output is
	// still scoped to the antigravity candidate set.
	repo := &capturedRepo2{rows: map[string]service.AvailabilityState{}}
	availSvc := service.NewPricingAvailabilityService(repo, time.Now)
	filter := service.NewModelListFilter(nil, availSvc) // pricing nil → fail-open (all pass)
	h := &GatewayHandler{tkModelListFilter: filter}

	result := h.tkAntigravityDefaultModels(context.Background())
	defaults := antigravity.DefaultModels()

	// Every returned model must come from the antigravity default set.
	defaultIDs := make(map[string]bool, len(defaults))
	for _, m := range defaults {
		defaultIDs[m.ID] = true
	}
	for _, m := range result {
		require.True(t, defaultIDs[m.ID],
			"output model %q is not in antigravity.DefaultModels() — cross-platform leakage", m.ID)
	}
}

func TestTkAntigravityDefaultModels_FilterDropsUnreachable(t *testing.T) {
	repo := &capturedRepo2{rows: map[string]service.AvailabilityState{}}
	availSvc := service.NewPricingAvailabilityService(repo, time.Now)

	defaults := antigravity.DefaultModels()
	require.NotEmpty(t, defaults, "test requires at least one antigravity model")
	targetID := defaults[0].ID

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
	pricingJSON := buildPricingJSON(defaults)
	pricingSvc := buildTestPricingService(t, pricingJSON)

	filter := service.NewModelListFilter(pricingSvc, availSvc)
	h := &GatewayHandler{tkModelListFilter: filter}

	result := h.tkAntigravityDefaultModels(context.Background())
	for _, m := range result {
		require.NotEqual(t, targetID, m.ID, "unreachable model must not appear in output")
	}
}

func TestTkAntigravityDefaultModels_NilFilterIsFailOpen(t *testing.T) {
	// When filter not wired, all default models must pass through.
	h := &GatewayHandler{}
	result := h.tkAntigravityDefaultModels(context.Background())
	defaults := antigravity.DefaultModels()
	require.Equal(t, len(defaults), len(result), "nil filter must be fail-open (all models pass)")
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
	h := &GatewayHandler{}
	result := h.tkGeminiFallbackModelsList(context.Background())
	defaults := gemini.DefaultModels()
	require.Equal(t, len(defaults), len(result.Models),
		"nil filter must be fail-open (all fallback Gemini models pass)")
}

func TestTkGeminiFallbackModelsList_FilterDropsUnreachable(t *testing.T) {
	repo := &capturedRepo2{rows: map[string]service.AvailabilityState{}}
	availSvc := service.NewPricingAvailabilityService(repo, time.Now)

	defaults := gemini.DefaultModels()
	require.NotEmpty(t, defaults)
	// targetID without "models/" prefix (that's what FilterClientFacing uses)
	targetWithPrefix := defaults[0].Name // "models/gemini-2.0-flash"
	targetID := targetWithPrefix[len("models/"):]

	availSvc.RecordOutcome(context.Background(), service.AvailabilityOutcome{
		Platform:           service.PlatformGemini,
		ModelID:            targetID,
		Success:            false,
		UpstreamStatusCode: 404,
		UpstreamErrorBody:  `{"error":{"message":"Requested entity was not found."}}`,
	})

	// Provide pricing service with all Gemini fallback models priced so the availability filter runs.
	geminiIDs := make([]string, len(defaults))
	for i, m := range defaults {
		geminiIDs[i] = m.Name[len("models/"):]
	}
	pricingJSON := buildPricingJSONFromIDs(geminiIDs)
	pricingSvc := buildTestPricingService(t, pricingJSON)

	filter := service.NewModelListFilter(pricingSvc, availSvc)
	h := &GatewayHandler{tkModelListFilter: filter}

	result := h.tkGeminiFallbackModelsList(context.Background())
	for _, m := range result.Models {
		require.NotEqual(t, targetWithPrefix, m.Name,
			"unreachable Gemini model must not appear in fallback response")
	}
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

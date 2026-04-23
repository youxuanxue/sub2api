//go:build unit

package handler

// US-027: GET /api/v1/public/pricing — public model + pricing catalog (PR1 v1 MVP).
// Spec: docs/approved/user-cold-start.md §2 v1; .testing/user-stories/stories/US-027-public-pricing-catalog.md.
//
// Each TestUS027_* function maps 1:1 to one Acceptance Criterion (AC-001 .. AC-005).
// AC-006 (regression of authenticated /v1/models) and AC-007 (whole-suite green) are
// covered by the existing TestGetAvailableModels_* and the build-tagged unit run.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCatalogSource lets each test inject the BuildPublicCatalog result directly,
// avoiding any dependency on filesystem fixtures or the real PricingCatalogService
// JSON parser. The parser itself is exercised separately in service-level tests.
type fakeCatalogSource struct {
	resp *service.PublicCatalogResponse
}

func (f *fakeCatalogSource) BuildPublicCatalog(_ context.Context) *service.PublicCatalogResponse {
	return f.resp
}

// fakeCatalogGate flips the public-route gate without touching the real
// SettingService (which would require a DB-backed setting cache).
type fakeCatalogGate struct {
	enabled bool
}

func (g *fakeCatalogGate) IsPricingCatalogPublic(_ context.Context) bool { return g.enabled }

func newPricingCatalogTestRouter(t *testing.T, gate PricingCatalogGate, src PricingCatalogSource) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &PricingCatalogHandler{catalog: src, gate: gate}
	r := gin.New()
	r.GET("/api/v1/public/pricing", h.GetPublicCatalog)
	return r
}

func sampleCatalog() *service.PublicCatalogResponse {
	return &service.PublicCatalogResponse{
		Object: "list",
		Data: []service.PublicCatalogModel{
			{
				ModelID: "claude-sonnet-4.5",
				Vendor:  "anthropic",
				Pricing: service.PublicCatalogPricing{
					Currency:          "USD",
					InputPer1KTokens:  0.003,
					OutputPer1KTokens: 0.015,
					CacheReadPer1K:    0.0003,
					CacheWritePer1K:   0.00375,
				},
				ContextWindow:   200000,
				MaxOutputTokens: 64000,
				Capabilities:    []string{"vision", "tool_use", "prompt_caching"},
			},
			{
				ModelID: "gpt-4o-mini",
				Vendor:  "openai",
				Pricing: service.PublicCatalogPricing{
					Currency:          "USD",
					InputPer1KTokens:  0.00015,
					OutputPer1KTokens: 0.0006,
				},
				ContextWindow:   128000,
				MaxOutputTokens: 16384,
				Capabilities:    []string{"vision", "tool_use"},
			},
		},
		UpdatedAt: time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC),
	}
}

// AC-001: Unauthenticated GET returns 200 + list shape with non-empty data.
func TestUS027_UnauthReturnsListShape(t *testing.T) {
	r := newPricingCatalogTestRouter(t,
		&fakeCatalogGate{enabled: true},
		&fakeCatalogSource{resp: sampleCatalog()},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/pricing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "must be 200, body=%s", w.Body.String())

	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "list", got["object"], "object field must be 'list' to mirror OpenAI /v1/models shape")
	data, ok := got["data"].([]any)
	require.True(t, ok, "data must be a JSON array, got %T", got["data"])
	assert.GreaterOrEqual(t, len(data), 1, "AC-001 requires non-empty data when fixture has entries")
	assert.Contains(t, got, "updated_at", "top-level updated_at is part of the public contract")
}

// AC-002: data[0] entry shape — required and optional fields per design §2 v1.
func TestUS027_EntryFieldsHaveExpectedShape(t *testing.T) {
	r := newPricingCatalogTestRouter(t,
		&fakeCatalogGate{enabled: true},
		&fakeCatalogSource{resp: sampleCatalog()},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/pricing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp service.PublicCatalogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.GreaterOrEqual(t, len(resp.Data), 1)

	first := resp.Data[0]
	assert.NotEmpty(t, first.ModelID, "model_id must be non-empty string")
	assert.Equal(t, "USD", first.Pricing.Currency, "currency MUST be USD (matches users.balance unit)")
	assert.GreaterOrEqual(t, first.Pricing.InputPer1KTokens, 0.0, "input price must be >= 0")
	assert.GreaterOrEqual(t, first.Pricing.OutputPer1KTokens, 0.0, "output price must be >= 0")
	assert.NotEmpty(t, first.Vendor, "vendor passes through when available in source")
	assert.Greater(t, first.ContextWindow, 0, "context_window passes through when available")
	assert.NotNil(t, first.Capabilities, "capabilities is always serialized as JSON array (never null)")
}

// AC-003: With pricing_catalog_public=false, return 404 (route hidden).
// Body must NOT contain `"object":"list"` — using 200+empty would leak the route's existence.
func TestUS027_DisabledBySetting404(t *testing.T) {
	r := newPricingCatalogTestRouter(t,
		&fakeCatalogGate{enabled: false},
		&fakeCatalogSource{resp: sampleCatalog()},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/pricing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code, "setting OFF must return 404, body=%s", w.Body.String())
	assert.NotContains(t, w.Body.String(), `"object":"list"`,
		"must NOT 200+empty when disabled — that would leak the route's existence")
}

// AC-004: Sensitive fields must never appear in the payload.
// Defensive: this fires if a future PR ever adds a `cost_per_token` (raw float)
// or similar internal field to the catalog DTO.
func TestUS027_NoSensitiveFieldsInPayload(t *testing.T) {
	r := newPricingCatalogTestRouter(t,
		&fakeCatalogGate{enabled: true},
		&fakeCatalogSource{resp: sampleCatalog()},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/pricing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	for _, sub := range []string{
		"account_id",
		"channel_type",
		"api_key",
		"access_token",
		"organization",
		"base_url",
		"cost_per_token",
	} {
		assert.False(t, strings.Contains(body, sub),
			"public catalog payload must not leak %q (found in body)", sub)
	}
}

// AC-005: When the catalog source has no data (startup race / load failure),
// return 200 + empty list. Never 500.
func TestUS027_EmptyCatalogReturnsEmptyList(t *testing.T) {
	cases := []struct {
		name string
		src  PricingCatalogSource
	}{
		{
			name: "source returns nil response",
			src:  &fakeCatalogSource{resp: nil},
		},
		{
			name: "source returns empty data slice",
			src: &fakeCatalogSource{resp: &service.PublicCatalogResponse{
				Object:    "list",
				Data:      []service.PublicCatalogModel{},
				UpdatedAt: time.Now().UTC(),
			}},
		},
		{
			name: "no source wired at all (startup race)",
			src:  nil,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r := newPricingCatalogTestRouter(t,
				&fakeCatalogGate{enabled: true},
				tc.src,
			)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/public/pricing", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code, "AC-005: degraded source must NEVER 500, got body=%s", w.Body.String())
			var resp service.PublicCatalogResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, "list", resp.Object)
			assert.Empty(t, resp.Data, "data must be []  when source has no entries")
		})
	}
}

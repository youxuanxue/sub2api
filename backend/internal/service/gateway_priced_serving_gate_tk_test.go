//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Tests for the runtime priced-serving gate (gateway_priced_serving_gate_tk.go +
// gateway_priced_serving_gate_wiring_tk.go), docs/approved/priced-or-it-doesnt-ship.md.

// gateRepoStub is a minimal SettingRepository whose only purpose is to return a
// fixed enabled-set string for SettingKeyPricedServingGateEnabled.
type gateRepoStub struct {
	values map[string]string
}

func newGateRepoStub() *gateRepoStub { return &gateRepoStub{values: map[string]string{}} }

func (s *gateRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	panic("unexpected Get call")
}
func (s *gateRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}
func (s *gateRepoStub) Set(ctx context.Context, key, value string) error {
	s.values[key] = value
	return nil
}
func (s *gateRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (s *gateRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	for k, v := range settings {
		s.values[k] = v
	}
	return nil
}
func (s *gateRepoStub) GetAll(ctx context.Context) (map[string]string, error) { return s.values, nil }
func (s *gateRepoStub) Delete(ctx context.Context, key string) error {
	delete(s.values, key)
	return nil
}

// newGateSettingService returns a SettingService whose enabled set is exactly
// `enabledSet` (empty string = setting row absent → no platform gated).
func newGateSettingService(enabledSet string) *SettingService {
	repo := newGateRepoStub()
	if enabledSet != "" {
		repo.values[SettingKeyPricedServingGateEnabled] = enabledSet
	}
	return NewSettingService(repo, &config.Config{})
}

// gateCatalogWith builds a PricingCatalogService whose catalog contains exactly
// the given model ids (each priced with a real non-zero input/output cost).
func gateCatalogWith(modelIDs ...string) *PricingCatalogService {
	svc := NewPricingCatalogService(nil)
	entries := map[string]map[string]any{}
	for _, id := range modelIDs {
		entries[id] = map[string]any{
			"input_cost_per_token":  0.000003,
			"output_cost_per_token": 0.000015,
			"litellm_provider":      "test",
		}
	}
	blob, _ := json.Marshal(entries)
	svc.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return blob, time.Now(), true
	})
	return svc
}

func newGateTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	return c, w
}

// ---------------------------------------------------------------------------
// tkPricedServingGateRejected — the pure decision (open/close × priced/unpriced).
// ---------------------------------------------------------------------------

func TestPricedServingGateRejected_Matrix(t *testing.T) {
	ctx := context.Background()
	catalog := gateCatalogWith("gemini-2.5-pro")

	cases := []struct {
		name       string
		enabledSet string
		platform   string
		model      string
		wantReject bool
	}{
		{"platform NOT in set: unpriced still served", "gemini", "openai", "gpt-unpriced", false},
		{"platform NOT in set: priced served", "gemini", "openai", "gemini-2.5-pro", false},
		{"empty set: nothing gated", "", "gemini", "gpt-unpriced", false},
		{"platform in set + unpriced: REJECT", "gemini", "gemini", "gpt-unpriced", true},
		{"platform in set + priced: pass", "gemini", "gemini", "gemini-2.5-pro", false},
		{"multi-member set, member + unpriced: REJECT", "gemini,openai", "openai", "gpt-unpriced", true},
		{"multi-member set, member + priced: pass", "gemini,openai", "openai", "gemini-2.5-pro", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := newGateSettingService(tc.enabledSet)
			got := tkPricedServingGateRejected(ctx, catalog, setting, tc.model, tc.platform)
			require.Equal(t, tc.wantReject, got)
		})
	}
}

func TestPricedServingGateRejected_NilDepsFailOpen(t *testing.T) {
	ctx := context.Background()
	setting := newGateSettingService("gemini")
	catalog := gateCatalogWith("gemini-2.5-pro")

	// Nil catalog or nil setting must never reject (additive subtraction must
	// not reject real traffic because of a wiring gap).
	require.False(t, tkPricedServingGateRejected(ctx, nil, setting, "gpt-unpriced", "gemini"))
	require.False(t, tkPricedServingGateRejected(ctx, catalog, nil, "gpt-unpriced", "gemini"))
}

// ---------------------------------------------------------------------------
// tkCheckPricedServingGate — full path: rejection writes 404 + returns false.
// ---------------------------------------------------------------------------

func TestCheckPricedServingGate_PassWhenPriced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("gemini")
	catalog := gateCatalogWith("gemini-2.5-pro")
	c, w := newGateTestContext()

	ok := tkCheckPricedServingGate(ctx, catalog, setting, nil, c, "gemini", "gemini-2.5-pro", "gemini-2.5-pro")
	require.True(t, ok, "priced model on enabled platform must pass")
	require.Equal(t, http.StatusOK, w.Code, "no response should be written on pass")
	require.False(t, c.IsAborted())
}

func TestCheckPricedServingGate_PassWhenPlatformDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("gemini") // openai NOT in set
	catalog := gateCatalogWith("gemini-2.5-pro")
	c, w := newGateTestContext()

	ok := tkCheckPricedServingGate(ctx, catalog, setting, nil, c, "openai", "gpt-unpriced", "gpt-unpriced")
	require.True(t, ok, "platform not in enabled set: serving unchanged even when unpriced")
	require.Equal(t, http.StatusOK, w.Code)
}

// ---------------------------------------------------------------------------
// 404 body byte-alignment per platform family (D1).
// ---------------------------------------------------------------------------

func TestCheckPricedServingGate_Reject_OpenAIShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("openai")
	catalog := gateCatalogWith("gemini-2.5-pro") // gpt-unpriced absent
	c, w := newGateTestContext()

	ok := tkCheckPricedServingGate(ctx, catalog, setting, nil, c, "openai", "gpt-unpriced", "gpt-unpriced")
	require.False(t, ok)
	require.Equal(t, http.StatusNotFound, w.Code, "HTTP status must be 404")
	require.True(t, c.IsAborted())

	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	errObj, ok := payload["error"].(map[string]any)
	require.True(t, ok, "OpenAI shape: top-level error object")
	require.Equal(t, "invalid_request_error", errObj["type"])
	require.Equal(t, tkPricedServingGateSubcode, errObj["code"], "subcode lives in body code field")
	require.Nil(t, payload["type"], "OpenAI shape has no top-level type")
}

func TestCheckPricedServingGate_Reject_NewAPIUsesOpenAIShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("newapi")
	catalog := gateCatalogWith("gemini-2.5-pro")
	c, w := newGateTestContext()

	ok := tkCheckPricedServingGate(ctx, catalog, setting, nil, c, "newapi", "qwen-unpriced", "qwen-unpriced")
	require.False(t, ok)
	require.Equal(t, http.StatusNotFound, w.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	errObj := payload["error"].(map[string]any)
	require.Equal(t, "invalid_request_error", errObj["type"], "newapi aligns to OpenAI-compat shape")
	require.Equal(t, tkPricedServingGateSubcode, errObj["code"])
}

func TestCheckPricedServingGate_Reject_AnthropicShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("anthropic")
	catalog := gateCatalogWith("claude-sonnet-4-6")
	c, w := newGateTestContext()

	ok := tkCheckPricedServingGate(ctx, catalog, setting, nil, c, "anthropic", "claude-unpriced", "claude-unpriced")
	require.False(t, ok)
	require.Equal(t, http.StatusNotFound, w.Code, "HTTP status must be 404 (not 4xx-other)")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Equal(t, "error", payload["type"], "Anthropic shape: top-level type=error")
	errObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "not_found_error", errObj["type"])
	_, hasCode := errObj["code"]
	require.False(t, hasCode, "Anthropic error envelope carries NO code field (subcode only in log)")
}

func TestCheckPricedServingGate_Reject_GeminiShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("gemini")
	catalog := gateCatalogWith("gemini-2.5-pro")
	c, w := newGateTestContext()

	ok := tkCheckPricedServingGate(ctx, catalog, setting, nil, c, "gemini", "gemini-unpriced", "gemini-unpriced")
	require.False(t, ok)
	require.Equal(t, http.StatusNotFound, w.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	errObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	// Gemini googleError shape: numeric code + NOT_FOUND status string.
	require.EqualValues(t, http.StatusNotFound, errObj["code"], "Gemini shape: numeric code 404")
	require.Equal(t, "NOT_FOUND", errObj["status"])
}

func TestCheckPricedServingGate_NilContextIsSafe(t *testing.T) {
	ctx := context.Background()
	setting := newGateSettingService("gemini")
	catalog := gateCatalogWith("gemini-2.5-pro")
	require.NotPanics(t, func() {
		// nil gin context: rejection path must not panic (returns false).
		ok := tkCheckPricedServingGate(ctx, catalog, setting, nil, nil, "gemini", "gpt-unpriced", "gpt-unpriced")
		require.False(t, ok)
	})
}

// TestCheckPricedServingGate_RejectFiresNotifier proves the v1 reject-time alert
// path: a rejection invokes the existing PricingMissingNotifier so ops gets the
// "model X unpriced, go price it" Feishu card (the v1 auto-pricing channel).
func TestCheckPricedServingGate_RejectFiresNotifier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("gemini")
	catalog := gateCatalogWith("gemini-2.5-pro")
	c, _ := newGateTestContext()

	spy := &gateNotifierSpy{}
	ok := tkCheckPricedServingGate(ctx, catalog, setting, spy, c, "gemini", "gemini-unpriced", "gemini-flash-orig")
	require.False(t, ok)
	require.Len(t, spy.events, 1, "rejection must fire exactly one pricing-missing event")
	ev := spy.events[0]
	require.Equal(t, tkPricedServingGateRejectReason, ev.Reason)
	require.Equal(t, "gemini-unpriced", ev.BillingModel)
	require.Equal(t, "gemini-flash-orig", ev.RequestedModel)
	require.Equal(t, "gemini", ev.Platform)
}

// gateNotifierSpy captures NotifyPricingMissing calls.
type gateNotifierSpy struct {
	events []PricingMissingEvent
}

func (s *gateNotifierSpy) NotifyPricingMissing(ev PricingMissingEvent) {
	s.events = append(s.events, ev)
}

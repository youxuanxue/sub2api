//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Tests for the runtime priced-serving gate (gateway_priced_serving_gate_tk.go +
// gateway_priced_serving_gate_wiring_tk.go), docs/approved/priced-or-it-doesnt-ship.md.
//
// Root-cause refactor (this revision): the gate judges via the SAME billing
// oracle billing uses — BillingService.GetModelPricing — not a catalog shadow
// predicate. So these tests inject a tkBillingPricingResolver (func over
// GetModelPricing). The canary model (tkPricedServingGateCanaryModel,
// "gemini-2.5-pro") must always resolve priced in a healthy resolver, so the
// "system degraded → fail-open" branch fires only when the WHOLE source is dead.

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

// gateBillingResolverWith builds a tkBillingPricingResolver backed by a REAL
// BillingService whose pricing source contains exactly the given model ids (each
// priced with a real non-zero input/output cost). Because it is the real
// GetModelPricing, family fallbacks ALSO apply (e.g. any "gemini-*" / "claude-*"
// resolves via getFallbackPricing even when absent from the source) — which is
// the whole point of the refactor. The canary gemini-2.5-pro therefore always
// resolves priced (gemini family fallback), so the degraded-source branch stays
// inert here.
func gateBillingResolverWith(modelIDs ...string) tkBillingPricingResolver {
	entries := map[string]map[string]any{}
	for _, id := range modelIDs {
		entries[id] = map[string]any{
			"input_cost_per_token":  0.000003,
			"output_cost_per_token": 0.000015,
			"litellm_provider":      "test",
		}
	}
	blob, _ := json.Marshal(entries)
	ps := &PricingService{}
	data, _ := ps.parsePricingData(blob)
	ps.pricingData = data
	billing := NewBillingService(nil, ps)
	return billing.GetModelPricing
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
	// Source has gemini-2.5-pro (also the canary) so healthy resolver resolves it.
	resolve := gateBillingResolverWith("gemini-2.5-pro")

	cases := []struct {
		name       string
		enabledSet string
		platform   string
		model      string
		wantReject bool
	}{
		{"platform NOT in set: unpriced still served", "gemini", "openai", "totally-unpriced-xyz", false},
		{"platform NOT in set: priced served", "gemini", "openai", "gemini-2.5-pro", false},
		{"empty set: nothing gated", "", "gemini", "totally-unpriced-xyz", false},
		{"platform in set + unpriced: REJECT", "gemini", "gemini", "totally-unpriced-xyz", true},
		{"platform in set + priced: pass", "gemini", "gemini", "gemini-2.5-pro", false},
		{"multi-member set, member + unpriced: REJECT", "gemini,openai", "openai", "totally-unpriced-xyz", true},
		{"multi-member set, member + priced: pass", "gemini,openai", "openai", "gemini-2.5-pro", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := newGateSettingService(tc.enabledSet)
			got := tkPricedServingGateRejected(ctx, resolve, nil, setting, tc.model, tc.platform, 0)
			require.Equal(t, tc.wantReject, got)
		})
	}
}

func TestPricedServingGateRejected_NilDepsFailOpen(t *testing.T) {
	ctx := context.Background()
	setting := newGateSettingService("gemini")
	resolve := gateBillingResolverWith("gemini-2.5-pro")

	// Nil resolver or nil setting must never reject (additive subtraction must not
	// reject real traffic because of a wiring gap).
	require.False(t, tkPricedServingGateRejected(ctx, nil, nil, setting, "totally-unpriced-xyz", "gemini", 0))
	require.False(t, tkPricedServingGateRejected(ctx, resolve, nil, nil, "totally-unpriced-xyz", "gemini", 0))
}

// TestPricedServingGate_GeminiUnknownServedAtFamilyFloor pins the post-pivot behavior (reverses the
// C-era "gemini unknown → reject", docs/approved/priced-or-it-doesnt-ship.md): a brand-new gemini-*
// id with no real price now falls to the gemini FAMILY FLOOR (getFallbackPricing), so it is SERVED
// (never $0, never 404). Only an id with NO family floor (multi-vendor newapi/国产 unknown) is
// rejected — the intended backstop. The degrade/canary fail-open mechanism was removed: the Go family
// floors are themselves the source-glitch protection (floored families can't mass-404).
func TestPricedServingGate_GeminiUnknownServedAtFamilyFloor(t *testing.T) {
	ctx := context.Background()
	setting := newGateSettingService("*")                // post-pivot default: all platforms enabled
	resolve := gateBillingResolverWith("gemini-2.5-pro") // REAL BillingService → Go family floors apply

	// A new gemini variant absent from the source now resolves via the gemini family floor.
	_, err := resolve("gemini-9.9-ultra-preview")
	require.False(t, errors.Is(err, ErrModelPricingUnavailable),
		"post-pivot: an unknown gemini variant falls to the gemini family floor (not unavailable)")
	require.False(t, tkPricedServingGateRejected(ctx, resolve, nil, setting, "gemini-9.9-ultra-preview", "gemini", 0),
		"a floored gemini model must be SERVED (at floor), not rejected")

	// A multi-vendor newapi/国产 unknown id has NO family floor → unavailable → rejected (backstop).
	_, err = resolve("some-unknown-vendor-model-zzz")
	require.True(t, errors.Is(err, ErrModelPricingUnavailable),
		"a no-family-match unknown id has no floor → billing unavailable")
	require.True(t, tkPricedServingGateRejected(ctx, resolve, nil, setting, "some-unknown-vendor-model-zzz", "newapi", 0),
		"a no-floor unknown id must be rejected (the backstop)")
}

// ---------------------------------------------------------------------------
// tkCheckPricedServingGate — full path: rejection writes 404 + returns false.
// ---------------------------------------------------------------------------

func TestCheckPricedServingGate_PassWhenPriced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("gemini")
	resolve := gateBillingResolverWith("gemini-2.5-pro")
	c, w := newGateTestContext()

	ok := tkCheckPricedServingGate(ctx, resolve, nil, setting, nil, c, tkGateWireGemini, "gemini", "gemini-2.5-pro", "gemini-2.5-pro")
	require.True(t, ok, "priced model on enabled platform must pass")
	require.Equal(t, http.StatusOK, w.Code, "no response should be written on pass")
	require.False(t, c.IsAborted())
}

func TestCheckPricedServingGate_PassWhenPlatformDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("gemini") // openai NOT in set
	resolve := gateBillingResolverWith("gemini-2.5-pro")
	c, w := newGateTestContext()

	ok := tkCheckPricedServingGate(ctx, resolve, nil, setting, nil, c, tkGateWireOpenAI, "openai", "gpt-unpriced", "gpt-unpriced")
	require.True(t, ok, "platform not in enabled set: serving unchanged even when unpriced")
	require.Equal(t, http.StatusOK, w.Code)
}

// ---------------------------------------------------------------------------
// 404 body byte-alignment per CLIENT WIRE PROTOCOL (D1 / BLOCKER4).
//
// Critical: the shape is chosen by the caller's wire protocol, NOT
// account.Platform — a gemini account can serve an Anthropic ingress, an
// anthropic account can serve an OpenAI ingress. These tests pass the protocol
// explicitly and assert the matching envelope.
// ---------------------------------------------------------------------------

func TestCheckPricedServingGate_Reject_OpenAIShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("openai")
	resolve := gateBillingResolverWith("gemini-2.5-pro") // the judged model has no family floor → reject
	c, w := newGateTestContext()

	// "no-family-unpriced-zzz" matches no Go family floor (not gpt/gemini/claude) → unavailable → reject.
	ok := tkCheckPricedServingGate(ctx, resolve, nil, setting, nil, c, tkGateWireOpenAI, "openai", "no-family-unpriced-zzz", "no-family-unpriced-zzz")
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

func TestCheckPricedServingGate_Reject_AnthropicShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("anthropic")
	resolve := gateBillingResolverWith("gemini-2.5-pro")
	c, w := newGateTestContext()

	ok := tkCheckPricedServingGate(ctx, resolve, nil, setting, nil, c, tkGateWireAnthropic, "anthropic", "totally-unpriced-xyz", "totally-unpriced-xyz")
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
	resolve := gateBillingResolverWith("gemini-2.5-pro")
	c, w := newGateTestContext()

	// totally-unpriced-xyz is not gemini-family so it really resolves unavailable.
	ok := tkCheckPricedServingGate(ctx, resolve, nil, setting, nil, c, tkGateWireGemini, "gemini", "totally-unpriced-xyz", "totally-unpriced-xyz")
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

// TestCheckPricedServingGate_WireProtocolDecouplesFromPlatform pins BLOCKER4 at
// the unit boundary: the SAME account platform ("gemini") gets DIFFERENT 404
// envelopes depending on the wire protocol the client speaks. A gemini account
// serving an Anthropic /v1/messages ingress must get the Anthropic envelope (not
// the Google one) or the Anthropic SDK reads error.type and throws an
// unexpected exception instead of a clean NotFoundError.
func TestCheckPricedServingGate_WireProtocolDecouplesFromPlatform(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	setting := newGateSettingService("gemini")
	resolve := gateBillingResolverWith("gemini-2.5-pro")

	// gemini account, Anthropic ingress → Anthropic envelope.
	c, w := newGateTestContext()
	ok := tkCheckPricedServingGate(ctx, resolve, nil, setting, nil, c, tkGateWireAnthropic, "gemini", "totally-unpriced-xyz", "totally-unpriced-xyz")
	require.False(t, ok)
	var anth map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &anth))
	require.Equal(t, "error", anth["type"], "gemini account on Anthropic ingress → Anthropic envelope")

	// gemini account, OpenAI ingress (ForwardAsChatCompletions) → OpenAI envelope.
	c2, w2 := newGateTestContext()
	ok2 := tkCheckPricedServingGate(ctx, resolve, nil, setting, nil, c2, tkGateWireOpenAI, "gemini", "totally-unpriced-xyz", "totally-unpriced-xyz")
	require.False(t, ok2)
	var oai map[string]any
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &oai))
	require.Nil(t, oai["type"], "gemini account on OpenAI ingress → OpenAI envelope (no top-level type)")
	errObj := oai["error"].(map[string]any)
	require.Equal(t, "invalid_request_error", errObj["type"])
}

func TestCheckPricedServingGate_NilContextIsSafe(t *testing.T) {
	ctx := context.Background()
	setting := newGateSettingService("gemini")
	resolve := gateBillingResolverWith("gemini-2.5-pro")
	require.NotPanics(t, func() {
		// nil gin context: rejection path must not panic (returns false).
		ok := tkCheckPricedServingGate(ctx, resolve, nil, setting, nil, nil, tkGateWireGemini, "gemini", "totally-unpriced-xyz", "totally-unpriced-xyz")
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
	resolve := gateBillingResolverWith("gemini-2.5-pro")
	c, _ := newGateTestContext()

	spy := &gateNotifierSpy{}
	ok := tkCheckPricedServingGate(ctx, resolve, nil, setting, spy, c, tkGateWireGemini, "gemini", "totally-unpriced-xyz", "gemini-flash-orig")
	require.False(t, ok)
	require.Len(t, spy.events, 1, "rejection must fire exactly one pricing-missing event")
	ev := spy.events[0]
	require.Equal(t, tkPricedServingGateRejectReason, ev.Reason)
	require.Equal(t, "totally-unpriced-xyz", ev.BillingModel)
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

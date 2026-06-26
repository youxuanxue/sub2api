//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Route-level tests for the priced-serving gate (docs/approved/priced-or-it-doesnt-ship.md).
//
// The companion tests (gateway_priced_serving_gate_tk_test.go) exercise the gate
// PREDICATE/COMPANION in isolation. These tests call the REAL Forward* methods to prove
// the gate is actually wired into each serving route, on billing's exact key, before the
// first byte. The adversarial review's #1 meta-finding was that the original tests stayed
// green even with a hook removed — these go red if a hook is deleted (verified: commenting
// out the gate call in Forward makes TestGeminiForward_Route_RejectsUnpricedOriginalUnderCatchAll
// fail, because the unpriced request would otherwise proceed and never 404).
//
// Construction is intentionally minimal: between each Forward* entry and the gate, only the
// request body and the *Account argument are touched — no service field is dereferenced — so
// a zero-value service with ONLY the gate deps set reaches the gate and rejects (early return,
// no nil panic). A genuinely-priced request would pass the gate and then nil-panic on the real
// forwarding deps, so only the REJECT (and the countTokens EXEMPT) paths are asserted here; the
// PASS path is covered by the companion tests.

// newGateBillingService builds a *BillingService (via the real constructor so fallbackPrices is
// populated for the degraded-source canary) priced ONLY for pricedIDs. Any other model resolves
// to ErrModelPricingUnavailable — i.e. billing would charge it $0, so the gate must reject it.
func newGateBillingService(t *testing.T, pricedIDs ...string) *BillingService {
	t.Helper()
	entries := map[string]map[string]any{}
	for _, id := range pricedIDs {
		entries[id] = map[string]any{
			"input_cost_per_token":  0.000003,
			"output_cost_per_token": 0.000015,
			"litellm_provider":      "test",
		}
	}
	blob, err := json.Marshal(entries)
	require.NoError(t, err)
	ps := &PricingService{}
	data, err := ps.parsePricingData(blob)
	require.NoError(t, err)
	ps.pricingData = data
	return NewBillingService(nil, ps)
}

// geminiGateService builds a GeminiMessagesCompatService with only the gate deps set
// (enabled set = "gemini", billing priced for the canary so the degraded-probe says healthy).
func geminiGateService(t *testing.T) *GeminiMessagesCompatService {
	t.Helper()
	svc := &GeminiMessagesCompatService{}
	svc.SetPricedServingGateDeps(
		nil,
		newGateBillingService(t, tkPricedServingGateCanaryModel),
		newGateSettingService("gemini"),
		nil,
	)
	return svc
}

// catchAllGeminiAccount is a gemini APIKey account whose model_mapping wildcard-maps EVERY
// client id onto a priced target — exactly the catch-all that BLOCKER1 was about.
func catchAllGeminiAccount() *Account {
	return &Account{
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"*": tkPricedServingGateCanaryModel},
		},
	}
}

// BLOCKER1 (the leak the gate exists to plug): under a catch-all mapping the mapped target is
// priced, but billing records the ORIGINAL id and would charge it $0. The gate must judge the
// original (billing's key) and reject — NOT the mapped target. If the gate judged the mapped
// model this test fails (mapped == canary == priced → no rejection).
func TestGeminiForward_Route_RejectsUnpricedOriginalUnderCatchAll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := geminiGateService(t)
	acct := catchAllGeminiAccount() // "*" -> priced canary
	c, w := newGateTestContext()
	body := []byte(`{"model":"garbage-unpriced-xyz","stream":false}`)

	_, err := svc.Forward(context.Background(), c, acct, body)

	require.Error(t, err, "gate must reject: original 'garbage-unpriced-xyz' is unpriced (billing key), even though it maps to a priced model")
	require.Equal(t, http.StatusNotFound, w.Code, "gate writes a 404 before forward")
	require.True(t, c.IsAborted(), "request must be aborted at the gate")
	// Forward serves an Anthropic /v1/messages ingress → 404 body must be the Anthropic envelope (BLOCKER4).
	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.Equal(t, "error", payload["type"], "Anthropic wire shape: top-level type=error")
}

// BLOCKER2: the third gemini ingress (OpenAI /v1/chat/completions onto a gemini account) had
// NO gate before round 2. Prove it now rejects an unpriced model, with the OpenAI envelope.
func TestGeminiForwardAsChatCompletions_Route_RejectsUnpriced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := geminiGateService(t)
	acct := &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}
	c, w := newGateTestContext()
	body := []byte(`{"model":"garbage-unpriced-xyz","messages":[{"role":"user","content":"hi"}]}`)

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, acct, body)

	require.Error(t, err, "previously-ungated chat-completions ingress must now reject unpriced")
	require.Equal(t, http.StatusNotFound, w.Code)
	require.True(t, c.IsAborted())
	// OpenAI ingress → OpenAI envelope.
	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	errObj, ok := payload["error"].(map[string]any)
	require.True(t, ok, "OpenAI wire shape: top-level error object")
	require.Equal(t, "invalid_request_error", errObj["type"])
}

// BLOCKER5 reverse: generateContent (real billing surface) must be GATED.
func TestGeminiForwardNative_Route_GatesGenerateContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := geminiGateService(t)
	acct := &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}
	c, w := newGateTestContext()

	_, err := svc.ForwardNative(context.Background(), c, acct, "garbage-unpriced-xyz", "generateContent", false, []byte(`{}`))

	require.Error(t, err, "generateContent is a real billing surface and must be gated")
	require.Equal(t, http.StatusNotFound, w.Code)
	require.True(t, c.IsAborted())
}

// BLOCKER5: countTokens is zero-billing + a never-hard-fail pre-flight contract → EXEMPT.
// The gate must NOT fire for it (no 404, no abort), even for an unpriced model. The post-gate
// path nil-panics on the real forwarding deps; we recover that and assert only that the gate
// itself did not reject (a removed exemption would 404 BEFORE any panic).
func TestGeminiForwardNative_Route_CountTokensExemptFromGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := geminiGateService(t)
	acct := &Account{Platform: PlatformGemini, Type: AccountTypeAPIKey}
	c, w := newGateTestContext()

	func() {
		defer func() { _ = recover() }() // post-exemption path touches nil deps; irrelevant to the gate decision
		_, _ = svc.ForwardNative(context.Background(), c, acct, "garbage-unpriced-xyz", "countTokens", false, []byte(`{}`))
	}()

	require.NotEqual(t, http.StatusNotFound, w.Code, "countTokens must be EXEMPT: gate must not 404 a zero-billing pre-flight")
	require.False(t, c.IsAborted(), "gate must not abort countTokens")
}

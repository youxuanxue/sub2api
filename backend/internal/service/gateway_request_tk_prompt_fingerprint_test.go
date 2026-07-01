package service

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkExtractAnthropicPromptFingerprint_GeoStegoInReminder(t *testing.T) {
	reminder := "<system-reminder>\nToday\u2019s date is 2026/06/30.\n</system-reminder>"
	body, err := json.Marshal(map[string]any{
		"system": []map[string]any{
			{"type": "text", "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK."},
		},
		"messages": []map[string]any{
			{"role": "user", "content": []map[string]any{
				{"type": "text", "text": reminder},
			}},
		},
	})
	require.NoError(t, err)
	fp := tkExtractAnthropicPromptFingerprint(body)
	require.Equal(t, 1, fp.SystemBlockCount)
	require.Equal(t, "claude_agent_sdk", fp.IdentityAnchorID)
	require.True(t, fp.HasSystemReminder)
	require.Equal(t, "SLASH_UNICODE", fp.ReminderDateLineClass)
	require.False(t, fp.GeoStegoCanonical)
	require.Contains(t, fp.UnknownSurfaces, "geo_stego_date_line")
	require.NotEmpty(t, fp.SurfaceSignature)
}

func TestTkExtractAnthropicPromptFingerprint_CanonicalAfterNormalize(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"Today\u2019s date is 2026/06/30."}]}]}`)
	out, changed := tkNormalizeAnthropicCCGeoStego(in)
	require.True(t, changed)
	fp := tkExtractAnthropicPromptFingerprint(out)
	require.Equal(t, "ISO_DASH_ASCII", fp.ReminderDateLineClass)
	require.True(t, fp.GeoStegoCanonical)
	require.Empty(t, fp.UnknownSurfaces)
}

func TestTkExtractAnthropicPromptFingerprint_BillingBlock(t *testing.T) {
	body := []byte(`{
		"system":[{"type":"text","text":"x-anthropic-billing-header: cc-session\nYou are Claude Code, Anthropic's official CLI for Claude."}]
	}`)
	fp := tkExtractAnthropicPromptFingerprint(body)
	require.True(t, fp.BillingPrefixPresent)
	require.Equal(t, "claude_code_cli", fp.IdentityAnchorID)
}

func TestTkExtractAnthropicPromptFingerprint_UnknownIdentity(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"You are a generic assistant."}]}`)
	fp := tkExtractAnthropicPromptFingerprint(body)
	require.Equal(t, tkIdentityAnchorUnknown, fp.IdentityAnchorID)
}

func TestTkPromptFingerprintShouldLog_OnNormalize(t *testing.T) {
	fp := tkExtractAnthropicPromptFingerprint([]byte(`{"messages":[]}`))
	require.True(t, fp.shouldLogPromptFingerprint(
		[]tkAnthropicNormalizeChange{tkNormalizeChangeCCGeoStego},
		"",
	))
}

func TestTkPromptFingerprintShouldLog_SampleWithoutSignal(t *testing.T) {
	fp := tkExtractAnthropicPromptFingerprint([]byte(`{"messages":[]}`))
	seen := 0
	for i := 0; i < 1000; i++ {
		id := "req-" + strings.Repeat("x", i)
		if fp.shouldLogPromptFingerprint(nil, id) {
			seen++
		}
	}
	require.Greater(t, seen, 0)
	require.Less(t, seen, 50)
}

func TestTkPromptFingerprintShouldLog_UnknownIdentitySamplesOnly(t *testing.T) {
	fp := tkExtractAnthropicPromptFingerprint([]byte(`{"system":[{"type":"text","text":"You are a generic assistant."}]}`))
	require.Equal(t, tkIdentityAnchorUnknown, fp.IdentityAnchorID)
	require.False(t, fp.shouldLogPromptFingerprint(nil, ""))
	seen := 0
	for i := 0; i < 1000; i++ {
		id := "req-" + strings.Repeat("y", i)
		if fp.shouldLogPromptFingerprint(nil, id) {
			seen++
		}
	}
	require.Greater(t, seen, 0)
	require.Less(t, seen, 50)
}

func TestTkPromptFingerprintShouldLog_UnknownIdentityWithBilling(t *testing.T) {
	fp := tkExtractAnthropicPromptFingerprint([]byte(`{
		"system":[{"type":"text","text":"x-anthropic-billing-header: cc-session\nYou are a custom agent."}]
	}`))
	require.Equal(t, tkIdentityAnchorUnknown, fp.IdentityAnchorID)
	require.True(t, fp.BillingPrefixPresent)
	require.True(t, fp.shouldLogPromptFingerprint(nil, ""))
}

func TestTkNormalizeAnthropicRequestBody_FingerprintCanonicalAfterGeo(t *testing.T) {
	svc := newNormalizeTestService(t, "true")
	in := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"Today\u2019s date is 2026/06/30."}]}]}`)
	out := svc.tkNormalizeAnthropicRequestBody(context.Background(), nil, in)
	fp := tkExtractAnthropicPromptFingerprint(out)
	require.Equal(t, "ISO_DASH_ASCII", fp.ReminderDateLineClass)
	require.True(t, fp.GeoStegoCanonical)
}

// TestTkProbePromptSurfaceGatewayCoverageJSONL is invoked by
// ops/anthropic/probe_prompt_surfaces.py --check-gateway via
// TOKENKEY_PROMPT_SURFACE_PROBE_JSONL=<capture.jsonl>.
func TestTkProbePromptSurfaceGatewayCoverageJSONL(t *testing.T) {
	path := os.Getenv("TOKENKEY_PROMPT_SURFACE_PROBE_JSONL")
	if path == "" {
		path = os.Getenv("TOKENKEY_CC_GEO_PROBE_JSONL")
	}
	if path == "" {
		t.Skip("TOKENKEY_PROMPT_SURFACE_PROBE_JSONL not set")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		scenario, _ := rec["scenario"].(string)
		baseURL, _ := rec["base_url"].(string)
		host, _ := rec["host"].(string)
		bodyWire, _ := rec["body_wire"].(map[string]any)
		if bodyWire == nil {
			continue
		}
		wire := map[string]any{
			"system":   bodyWire["system"],
			"messages": bodyWire["messages"],
		}
		if wire["system"] == nil && wire["messages"] == nil {
			continue
		}
		bodyBytes, err := json.Marshal(wire)
		require.NoError(t, err)

		out, changed := tkNormalizeAnthropicCCGeoStego(bodyBytes)
		_, still := tkNormalizeAnthropicCCGeoStego(out)
		require.False(t, still, "normalize not idempotent for scenario=%s", scenario)

		isFirstPartyControl := strings.Contains(baseURL, "api.anthropic.com") &&
			strings.Contains(host, "anthropic.com")
		if isFirstPartyControl {
			require.False(t, changed, "first-party anthropic.com control must stay untouched: %s", scenario)
			continue
		}
		require.False(t, tkWireStillHasCCGeoStegoDateSignals(out),
			"gateway left geo stego in output for scenario=%s", scenario)

		fp := tkExtractAnthropicPromptFingerprint(out)
		require.True(t, fp.GeoStegoCanonical, "fingerprint not canonical for scenario=%s", scenario)
		require.NotContains(t, fp.UnknownSurfaces, "geo_stego_date_line", "scenario=%s", scenario)
	}
}

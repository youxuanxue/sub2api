//go:build unit

package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestResolveMoonshotThinkingFamily(t *testing.T) {
	cases := []struct {
		model string
		want  moonshotThinkingFamily
	}{
		{"kimi-k3", moonshotThinkingK3},
		{"Kimi-K3", moonshotThinkingK3},
		{"kimi/kimi-k3", moonshotThinkingK3},
		{"kimi-k3-preview", moonshotThinkingK3},
		{"kimi-k2.7-code", moonshotThinkingK27Code},
		{"kimi-k2.7-code-highspeed", moonshotThinkingK27Code},
		{"kimi-k2.6", moonshotThinkingK26},
		{"kimi-k2.5", moonshotThinkingK25},
		{"kimi-latest", moonshotThinkingKimiOther},
		{"moonshot-v1-128k", moonshotThinkingKimiOther},
		{"gpt-4o", moonshotThinkingNone},
		{"", moonshotThinkingNone},
	}
	for _, tc := range cases {
		if got := resolveMoonshotThinkingFamily(tc.model); got != tc.want {
			t.Fatalf("resolveMoonshotThinkingFamily(%q)=%v want %v", tc.model, got, tc.want)
		}
	}
}

func TestNormalizeMoonshotThinking_BoolToObject(t *testing.T) {
	body := []byte(`{"model":"kimi-k2.5","thinking":true,"messages":[{"role":"user","content":"hi"}]}`)
	got, err := NormalizeMoonshotThinking("kimi-k2.5", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if typ := gjson.GetBytes(got, "thinking.type").String(); typ != "enabled" {
		t.Fatalf("thinking.type=%q want enabled", typ)
	}

	body = []byte(`{"model":"kimi-k2.5","thinking":false,"messages":[{"role":"user","content":"hi"}]}`)
	got, err = NormalizeMoonshotThinking("kimi-k2.5", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if typ := gjson.GetBytes(got, "thinking.type").String(); typ != "disabled" {
		t.Fatalf("thinking.type=%q want disabled", typ)
	}
}

func TestNormalizeMoonshotThinking_K3StripsThinkingKeepsEffort(t *testing.T) {
	body := []byte(`{"model":"kimi-k3","thinking":true,"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	got, err := NormalizeMoonshotThinking("kimi-k3", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson.GetBytes(got, "thinking").Exists() {
		t.Fatal("k3 must strip thinking")
	}
	if gjson.GetBytes(got, "reasoning_effort").String() != "high" {
		t.Fatalf("reasoning_effort=%q want high", gjson.GetBytes(got, "reasoning_effort").String())
	}
}

func TestNormalizeMoonshotThinking_K3RejectsDisableAndBadEffort(t *testing.T) {
	_, err := NormalizeMoonshotThinking("kimi-k3", []byte(`{"thinking":false}`))
	var inv *OpenAIInvalidParameterError
	if !errors.As(err, &inv) || inv.Param != "thinking" {
		t.Fatalf("want thinking invalid_parameter, got %v", err)
	}

	_, err = NormalizeMoonshotThinking("kimi-k3", []byte(`{"reasoning_effort":"medium"}`))
	if !errors.As(err, &inv) || inv.Param != "reasoning_effort" {
		t.Fatalf("want reasoning_effort invalid_parameter, got %v", err)
	}
}

func TestNormalizeMoonshotThinking_K2StripsReasoningEffort(t *testing.T) {
	body := []byte(`{"model":"kimi-k2.5","thinking":{"type":"enabled"},"reasoning_effort":"high"}`)
	got, err := NormalizeMoonshotThinking("kimi-k2.5", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson.GetBytes(got, "reasoning_effort").Exists() {
		t.Fatal("k2.5 must strip reasoning_effort")
	}
	if gjson.GetBytes(got, "thinking.type").String() != "enabled" {
		t.Fatal("thinking must remain")
	}
}

func TestNormalizeMoonshotThinking_K27RejectsDisabled(t *testing.T) {
	_, err := NormalizeMoonshotThinking("kimi-k2.7-code", []byte(`{"thinking":{"type":"disabled"}}`))
	var inv *OpenAIInvalidParameterError
	if !errors.As(err, &inv) || inv.Param != "thinking" {
		t.Fatalf("want thinking invalid_parameter, got %v", err)
	}
}

func TestNormalizeMoonshotThinking_K27OmitsValidThinking(t *testing.T) {
	body := []byte(`{"thinking":{"type":"enabled","keep":"all"},"messages":[{"role":"user","content":"hi"}]}`)
	got, err := NormalizeMoonshotThinking("kimi-k2.7-code", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson.GetBytes(got, "thinking").Exists() {
		t.Fatal("valid k2.7 thinking should be omitted (always-on)")
	}
}

func TestNormalizeMoonshotThinking_LeavesNonMoonshot(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","thinking":true}`)
	got, err := NormalizeMoonshotThinking("gpt-4o", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("non-moonshot body must be unchanged")
	}
}

func TestNormalizeMoonshotThinking_RejectsStringThinking(t *testing.T) {
	_, err := NormalizeMoonshotThinking("kimi-k2.5", []byte(`{"thinking":"enabled"}`))
	var inv *OpenAIInvalidParameterError
	if !errors.As(err, &inv) || inv.Param != "thinking" {
		t.Fatalf("want thinking invalid_parameter, got %v", err)
	}
}

func TestApplyMoonshotThinkingShape_WritesParamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	_, err := applyMoonshotThinkingShape(c, "kimi-k3", []byte(`{"thinking":false}`))
	var inv *OpenAIInvalidParameterError
	if !errors.As(err, &inv) {
		t.Fatalf("want OpenAIInvalidParameterError, got %v", err)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", w.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errObj, _ := payload["error"].(map[string]any)
	if errObj["param"] != "thinking" {
		t.Fatalf("param=%v want thinking", errObj["param"])
	}
	if errObj["code"] != "invalid_parameter" {
		t.Fatalf("code=%v want invalid_parameter", errObj["code"])
	}
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("type=%v want invalid_request_error", errObj["type"])
	}
	if !IsResponseCommitted(c) {
		t.Fatal("response must be marked committed")
	}
}

func TestNormalizeMoonshotThinking_PromotesNestedEffort(t *testing.T) {
	body := []byte(`{"model":"kimi-k3","reasoning":{"effort":"low"}}`)
	got, err := NormalizeMoonshotThinking("kimi-k3", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson.GetBytes(got, "reasoning_effort").String() != "low" {
		t.Fatalf("reasoning_effort=%q want low", gjson.GetBytes(got, "reasoning_effort").String())
	}
	if gjson.GetBytes(got, "reasoning.effort").Exists() {
		t.Fatal("nested reasoning.effort should be promoted away")
	}
}

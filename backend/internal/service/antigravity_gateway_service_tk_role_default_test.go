//go:build unit

package service

import (
	"encoding/json"
	"testing"
)

func TestTkEnsureGeminiContentRoles(t *testing.T) {
	roleOf := func(t *testing.T, body []byte, idx int) (string, bool) {
		t.Helper()
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		contents, _ := payload["contents"].([]any)
		if idx >= len(contents) {
			t.Fatalf("content idx %d out of range (len=%d)", idx, len(contents))
		}
		c, _ := contents[idx].(map[string]any)
		r, ok := c["role"]
		if !ok {
			return "", false
		}
		s, _ := r.(string)
		return s, true
	}

	t.Run("missing role defaults to user (the prod 400 → 200 fix)", func(t *testing.T) {
		in := []byte(`{"contents":[{"parts":[{"text":"Reply OK"}]}]}`)
		out := tkEnsureGeminiContentRoles(in)
		role, ok := roleOf(t, out, 0)
		if !ok || role != "user" {
			t.Fatalf("expected role=user, got role=%q ok=%v", role, ok)
		}
	})

	t.Run("explicit role is preserved (user)", func(t *testing.T) {
		in := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
		out := tkEnsureGeminiContentRoles(in)
		role, _ := roleOf(t, out, 0)
		if role != "user" {
			t.Fatalf("expected role preserved=user, got %q", role)
		}
	})

	t.Run("explicit model role is never overwritten", func(t *testing.T) {
		in := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"ok"}]}]}`)
		out := tkEnsureGeminiContentRoles(in)
		if r, _ := roleOf(t, out, 0); r != "user" {
			t.Fatalf("content[0] role changed: %q", r)
		}
		if r, _ := roleOf(t, out, 1); r != "model" {
			t.Fatalf("content[1] model role overwritten: %q", r)
		}
	})

	t.Run("empty-string role is treated as missing and defaulted", func(t *testing.T) {
		in := []byte(`{"contents":[{"role":"","parts":[{"text":"hi"}]}]}`)
		out := tkEnsureGeminiContentRoles(in)
		role, _ := roleOf(t, out, 0)
		if role != "user" {
			t.Fatalf("expected empty role defaulted to user, got %q", role)
		}
	})

	t.Run("mixed: only the role-less entry gets a role", func(t *testing.T) {
		in := []byte(`{"contents":[{"role":"model","parts":[{"text":"a"}]},{"parts":[{"text":"b"}]}]}`)
		out := tkEnsureGeminiContentRoles(in)
		if r, _ := roleOf(t, out, 0); r != "model" {
			t.Fatalf("content[0] role changed: %q", r)
		}
		if r, _ := roleOf(t, out, 1); r != "user" {
			t.Fatalf("content[1] should default to user, got %q", r)
		}
	})

	t.Run("invalid JSON returns original body untouched", func(t *testing.T) {
		in := []byte(`{not json`)
		out := tkEnsureGeminiContentRoles(in)
		if string(out) != string(in) {
			t.Fatalf("expected passthrough on invalid JSON, got %q", out)
		}
	})

	t.Run("no contents key returns original body untouched", func(t *testing.T) {
		in := []byte(`{"systemInstruction":{"parts":[{"text":"x"}]}}`)
		out := tkEnsureGeminiContentRoles(in)
		if string(out) != string(in) {
			t.Fatalf("expected passthrough when contents absent, got %q", out)
		}
	})

	t.Run("all roles present returns original bytes (no rewrite)", func(t *testing.T) {
		in := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
		out := tkEnsureGeminiContentRoles(in)
		if string(out) != string(in) {
			t.Fatalf("expected identical bytes when no change needed, got %q", out)
		}
	})
}

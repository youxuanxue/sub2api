//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestTkStripEncryptedReasoningForFailover_FastPathNoMarker(t *testing.T) {
	// A Responses body with reasoning but no encrypted_content must be returned
	// byte-for-byte without any parse/re-marshal.
	body := []byte(`{"model":"o3","input":[{"type":"reasoning","summary":[]},{"type":"message","role":"user","content":"hi"}]}`)
	got := TkStripEncryptedReasoningForFailover(body)
	if !bytes.Equal(got, body) {
		t.Fatalf("expected body unchanged on fast path, got %s", got)
	}
}

func TestTkStripEncryptedReasoningForFailover_StripsEncryptedContent(t *testing.T) {
	// reasoning item carries other fields besides encrypted_content -> the item
	// is kept but encrypted_content is dropped.
	body := []byte(`{"model":"o3","input":[{"type":"reasoning","id":"rs_1","encrypted_content":"abc","summary":["s"]},{"type":"message","role":"user","content":"hi"}]}`)
	got := TkStripEncryptedReasoningForFailover(body)
	if bytes.Contains(got, []byte("encrypted_content")) {
		t.Fatalf("encrypted_content should be stripped, got %s", got)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	input, ok := parsed["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected 2 input items preserved, got %#v", parsed["input"])
	}
	reasoning, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("first item not an object: %#v", input[0])
	}
	if _, has := reasoning["encrypted_content"]; has {
		t.Fatalf("encrypted_content not removed from reasoning item: %#v", reasoning)
	}
	if reasoning["id"] != "rs_1" {
		t.Fatalf("non-encrypted fields must be preserved, got %#v", reasoning)
	}
}

func TestTkStripEncryptedReasoningForFailover_DropsBareReasoningItem(t *testing.T) {
	// reasoning item whose only payload was encrypted_content collapses to empty
	// and is removed entirely.
	body := []byte(`{"model":"o3","input":[{"type":"reasoning","encrypted_content":"abc"},{"type":"message","role":"user","content":"hi"}]}`)
	got := TkStripEncryptedReasoningForFailover(body)

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	input, ok := parsed["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("expected bare reasoning item dropped (1 item left), got %#v", parsed["input"])
	}
	msg, ok := input[0].(map[string]any)
	if !ok || msg["type"] != "message" {
		t.Fatalf("surviving item should be the message, got %#v", input[0])
	}
}

func TestTkStripEncryptedReasoningForFailover_ChatCompletionsBodyUntouched(t *testing.T) {
	// A Chat Completions body has no top-level `input` array, so even if the
	// literal substring appears (e.g. inside message text) nothing is stripped.
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"what is encrypted_content?"}]}`)
	got := TkStripEncryptedReasoningForFailover(body)
	if !bytes.Equal(got, body) {
		t.Fatalf("chat completions body must be untouched, got %s", got)
	}
}

func TestTkStripEncryptedReasoningForFailover_MalformedBodyReturnedAsIs(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","encrypted_content":`) // truncated, contains marker
	got := TkStripEncryptedReasoningForFailover(body)
	if !bytes.Equal(got, body) {
		t.Fatalf("malformed body must be returned unchanged, got %s", got)
	}
}

func TestTkStripEncryptedReasoningForFailover_EmptyBody(t *testing.T) {
	if got := TkStripEncryptedReasoningForFailover(nil); got != nil {
		t.Fatalf("nil body must round-trip as nil, got %s", got)
	}
}

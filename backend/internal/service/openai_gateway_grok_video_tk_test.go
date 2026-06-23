//go:build unit

package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestReadGrokVideoResponseLimited pins the bounded read that protects the grok
// video arm from a hostile/runaway upstream: a body within the cap reads back
// verbatim, and one over the cap returns errGrokVideoResponseTooLarge instead of
// buffering unbounded media into gateway memory (parity with the new-api bridge's
// readVideoFetchResponseBodyLimited).
func TestReadGrokVideoResponseLimited(t *testing.T) {
	t.Run("within cap reads verbatim", func(t *testing.T) {
		body := `{"video_url":"https://x.ai/v.mp4"}`
		got, err := readGrokVideoResponseLimited(strings.NewReader(body), 1024)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != body {
			t.Fatalf("body mismatch: got %q want %q", got, body)
		}
	})
	t.Run("over cap errors", func(t *testing.T) {
		_, err := readGrokVideoResponseLimited(strings.NewReader(strings.Repeat("a", 100)), 16)
		if !errors.Is(err, errGrokVideoResponseTooLarge) {
			t.Fatalf("expected errGrokVideoResponseTooLarge, got %v", err)
		}
	})
	t.Run("exactly at cap is allowed", func(t *testing.T) {
		body := strings.Repeat("a", 16)
		got, err := readGrokVideoResponseLimited(strings.NewReader(body), 16)
		if err != nil || len(got) != 16 {
			t.Fatalf("at-cap read should succeed: got len=%d err=%v", len(got), err)
		}
	})
}

// TestNormalizeGrokVideoStatus pins the mapping from xAI's video status enum
// (queued/processing/done/failed/expired) onto the handler's videoTerminalOutcome
// vocabulary (success/failure/non-terminal-passthrough). A drift here would
// either skip terminal-success S3 retention or skip the terminal-failure refund.
func TestNormalizeGrokVideoStatus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// terminal success (case-insensitive)
		{"done", "success"},
		{"Done", "success"},
		{"success", "success"},
		{"succeeded", "success"},
		{"completed", "success"},
		// terminal failure
		{"failed", "failure"},
		{"failure", "failure"},
		{"canceled", "failure"},
		{"cancelled", "failure"},
		// non-terminal: passthrough verbatim so the poller keeps polling.
		// "expired" is intentionally HERE (not failure): refunding on expired
		// would leak money when it follows a billed-and-kept "done" (result TTL).
		{"expired", "expired"},
		{"queued", "queued"},
		{"processing", "processing"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizeGrokVideoStatus(tc.in); got != tc.want {
				t.Fatalf("normalizeGrokVideoStatus(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestBuildGrokVideoSubmitResponse verifies the synchronous submit
// acknowledgement carries TK's PUBLIC task id (the client polls
// GET /v1/videos/{id} with it) and the OpenAI-Video submit shape the handler
// contract expects (queued / progress 0 / created_at stamped).
func TestBuildGrokVideoSubmitResponse(t *testing.T) {
	const publicID = "vt_abc123"
	const model = "grok-imagine-video"

	raw := buildGrokVideoSubmitResponse(publicID, model)

	var got struct {
		ID        string `json:"id"`
		Object    string `json:"object"`
		Model     string `json:"model"`
		Status    string `json:"status"`
		Progress  int    `json:"progress"`
		CreatedAt int64  `json:"created_at"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("submit ack is not valid JSON: %v (%s)", err, raw)
	}
	if got.ID != publicID {
		t.Fatalf("id = %q, want public task id %q (client must poll with TK's id, not the upstream request_id)", got.ID, publicID)
	}
	if got.Object != "video" {
		t.Fatalf("object = %q, want %q", got.Object, "video")
	}
	if got.Model != model {
		t.Fatalf("model = %q, want %q", got.Model, model)
	}
	if got.Status != "queued" {
		t.Fatalf("status = %q, want %q", got.Status, "queued")
	}
	if got.Progress != 0 {
		t.Fatalf("progress = %d, want 0", got.Progress)
	}
	if got.CreatedAt <= 0 {
		t.Fatalf("created_at = %d, want a positive unix timestamp", got.CreatedAt)
	}
}

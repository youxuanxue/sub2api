//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestResolveGrokVideoCredential(t *testing.T) {
	t.Run("oauth uses access_token", func(t *testing.T) {
		acct := &Account{
			Platform: PlatformGrok,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"access_token": "oauth-bearer",
				"base_url":     "https://api.x.ai/v1",
			},
		}
		token, base, err := resolveGrokVideoCredential(acct)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if token != "oauth-bearer" || base != "https://api.x.ai/v1" {
			t.Fatalf("got token=%q base=%q", token, base)
		}
	})

	t.Run("apikey relay uses edge api_key", func(t *testing.T) {
		acct := &Account{
			ID:       65,
			Platform: PlatformGrok,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_key":  "edge-relay-key",
				"base_url": "https://api-us4.tokenkey.dev",
			},
		}
		token, base, err := resolveGrokVideoCredential(acct)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if token != "edge-relay-key" || base != "https://api-us4.tokenkey.dev" {
			t.Fatalf("got token=%q base=%q", token, base)
		}
	})

	t.Run("legacy newapi edge relay stub uses api_key", func(t *testing.T) {
		acct := &Account{
			ID:          65,
			Platform:    PlatformNewAPI,
			Type:        AccountTypeAPIKey,
			ChannelType: 1,
			Credentials: map[string]any{
				"api_key":  "grok-bridge-key",
				"base_url": "https://api-us4.tokenkey.dev",
			},
		}
		if !UsesGrokNativeVideoArm(acct) {
			t.Fatal("legacy grok bridge stub must use grok native video arm")
		}
		token, base, err := resolveGrokVideoCredential(acct)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if token != "grok-bridge-key" || base != "https://api-us4.tokenkey.dev" {
			t.Fatalf("got token=%q base=%q", token, base)
		}
	})

	t.Run("kiro anthropic edge mirror is not grok video relay", func(t *testing.T) {
		acct := &Account{
			ID:       8,
			Platform: PlatformAnthropic,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_key":         "kiro-edge-key",
				"base_url":        "https://api-us4.tokenkey.dev",
				"mirror_platform": "kiro",
			},
		}
		if UsesGrokNativeVideoArm(acct) {
			t.Fatal("kiro mirror stub must not route to grok native video arm")
		}
	})

	t.Run("oauth missing access_token", func(t *testing.T) {
		acct := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}
		_, _, err := resolveGrokVideoCredential(acct)
		if err == nil || !strings.Contains(err.Error(), "missing access_token") {
			t.Fatalf("want missing access_token error, got %v", err)
		}
	})

	t.Run("relay missing api_key", func(t *testing.T) {
		acct := &Account{
			ID:       65,
			Platform: PlatformGrok,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"base_url": "https://api-us4.tokenkey.dev",
			},
		}
		_, _, err := resolveGrokVideoCredential(acct)
		if err == nil || !strings.Contains(err.Error(), "missing api_key") {
			t.Fatalf("want missing api_key error, got %v", err)
		}
	})

	t.Run("oauth default base when unset", func(t *testing.T) {
		acct := &Account{
			Platform: PlatformGrok,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"access_token": "tok",
			},
		}
		_, base, err := resolveGrokVideoCredential(acct)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if base != xai.DefaultBaseURL {
			t.Fatalf("base = %q, want %q", base, xai.DefaultBaseURL)
		}
	})
}

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

func TestNormalizeGrokVideoSubmitBodyMapsOpenAICompatDuration(t *testing.T) {
	raw := []byte(`{"model":"grok-imagine-video","prompt":"waves","duration_seconds":"4.2","seconds":"4"}`)
	got, err := normalizeGrokVideoSubmitBody(raw)
	if err != nil {
		t.Fatalf("normalizeGrokVideoSubmitBody error: %v", err)
	}
	if strings.Contains(string(got), "duration_seconds") || strings.Contains(string(got), "seconds") {
		t.Fatalf("OpenAI-compat duration aliases must be stripped before xAI submit: %s", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("normalized body is not JSON: %v (%s)", err, got)
	}
	if payload["duration"] != float64(4) {
		t.Fatalf("duration = %#v, want 4 from seconds alias", payload["duration"])
	}
}

func TestBuildGrokVideoV1URLs(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		submit string
		fetch  string
	}{
		{
			name:   "edge root base",
			base:   "https://api-us4.tokenkey.dev",
			submit: "https://api-us4.tokenkey.dev/v1/videos/generations",
			fetch:  "https://api-us4.tokenkey.dev/v1/videos/req-123",
		},
		{
			name:   "v1 base",
			base:   "https://api.x.ai/v1",
			submit: "https://api.x.ai/v1/videos/generations",
			fetch:  "https://api.x.ai/v1/videos/req-123",
		},
		{
			name:   "trailing slash v1 base",
			base:   "https://api.x.ai/v1/",
			submit: "https://api.x.ai/v1/videos/generations",
			fetch:  "https://api.x.ai/v1/videos/req-123",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildGrokVideoSubmitURL(tc.base); got != tc.submit {
				t.Fatalf("submit URL = %q, want %q", got, tc.submit)
			}
			if got := buildGrokVideoFetchURL(tc.base, "req-123"); got != tc.fetch {
				t.Fatalf("fetch URL = %q, want %q", got, tc.fetch)
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

func TestGrokVideoUpstreamErrorEntitlement403QuarantinesAccount(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 8801, Platform: PlatformGrok, Type: AccountTypeOAuth}
	before := time.Now()
	body := []byte(`{"code":"forbidden","error":"You have either run out of available resources or do not have an active Grok subscription."}`)

	err := svc.grokVideoUpstreamError(context.Background(), account, http.StatusForbidden, body)
	require.Error(t, err)
	require.Equal(t, 1, repo.tempUnschedCalls)
	require.Equal(t, "grok access or entitlement denied", repo.lastTempUnschedReason)
	require.Greater(t, repo.lastTempUnschedUntil, before.Add(23*time.Hour))
	require.Less(t, repo.lastTempUnschedUntil, before.Add(25*time.Hour))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

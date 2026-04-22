//go:build unit

package service

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// US-023 (round-2 audit) — round 2 fix verification.
//
// Round-1 audit (US-022) closed five admin-lifecycle gaps. Round-2 audit then
// found two more silent fall-through bugs that hurt newapi correctness at the
// runtime layer (not admin layer):
//
//   1. ratelimit_service.handle429: when upstream returns 429 without an
//      anthropic-ratelimit-unified-reset header, the body-parse switch only
//      handles PlatformOpenAI / PlatformGemini / PlatformAntigravity. NewAPI
//      falls into the "no reset time" branch and gets a default 5-minute lock
//      — the OpenAI-compat usage_limit_reached body (which new-api adaptors
//      forward verbatim) is silently ignored. This significantly under-counts
//      reset and causes immediate re-hammering on the same upstream key.
//
//   2. ops_retry.detectOpsRetryType: classifies /v1/chat/completions as
//      opsRetryTypeMessages (the catch-all default), so admin "retry with
//      account" on a chat/completions error tries to parse the OpenAI-shape
//      body as Anthropic and call gatewayService.Forward (Anthropic forwarder)
//      against the OpenAI/NewAPI account. Always fails. The fix classifies
//      chat/completions as opsRetryTypeOpenAI, plus a defense-in-depth guard
//      in executeWithAccount messages-default that fails fast on
//      OpenAI/NewAPI accounts instead of silently calling the wrong forwarder.

type us023NewAPI429Repo struct {
	mockAccountRepoForGemini
	rateLimitedID    int64
	rateLimitedAt    time.Time
	rateLimitedCalls int
}

func (r *us023NewAPI429Repo) SetRateLimited(_ context.Context, id int64, t time.Time) error {
	r.rateLimitedID = id
	r.rateLimitedAt = t
	r.rateLimitedCalls++
	return nil
}

func TestUS023_NewAPI_Handle429_ParsesOpenAICompatBody(t *testing.T) {
	repo := &us023NewAPI429Repo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 4242, Platform: PlatformNewAPI, ChannelType: 1, Type: AccountTypeAPIKey}

	// New API adaptor returns OpenAI-shape usage_limit_reached body. Compute a
	// known reset 1h in the future to assert the parser actually used it.
	expectedResetUnix := time.Now().Add(1 * time.Hour).Unix()
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"limit reached","resets_at":` +
		strconv.FormatInt(expectedResetUnix, 10) + `}}`)
	headers := http.Header{} // no anthropic-ratelimit-unified-reset, no x-codex-* headers

	svc.handle429(context.Background(), account, headers, body)

	if repo.rateLimitedCalls != 1 {
		t.Fatalf("expected SetRateLimited called once, got %d", repo.rateLimitedCalls)
	}
	if repo.rateLimitedID != account.ID {
		t.Fatalf("rateLimitedID = %d, want %d", repo.rateLimitedID, account.ID)
	}
	got := repo.rateLimitedAt.Unix()
	if got != expectedResetUnix {
		t.Fatalf("rateLimitedAt = %d (delta=%ds vs default-5m fallback would be ~%ds), want %d — handler likely fell into default 5-minute branch instead of OpenAI body parse",
			got,
			got-time.Now().Unix(),
			5*60,
			expectedResetUnix)
	}
}

func TestUS023_NewAPI_Handle429_FallsBackTo5MinWhenBodyHasNoResetTime(t *testing.T) {
	// Negative side: if the body genuinely lacks resets_at / resets_in_seconds,
	// the default 5-minute lock is still the documented fallback for newapi.
	// This guards against an over-eager "always parse" mistake in the fix.
	repo := &us023NewAPI429Repo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 4243, Platform: PlatformNewAPI, ChannelType: 1, Type: AccountTypeAPIKey}

	// usage_limit_reached but no resets_at / resets_in_seconds → parser returns
	// nil, handler should still set rate-limited with default 5min.
	body := []byte(`{"error":{"type":"some_other_error","message":"oops"}}`)
	headers := http.Header{}

	before := time.Now()
	svc.handle429(context.Background(), account, headers, body)
	after := time.Now()

	if repo.rateLimitedCalls != 1 {
		t.Fatalf("expected SetRateLimited called once, got %d", repo.rateLimitedCalls)
	}
	wantMin := before.Add(5 * time.Minute).Add(-1 * time.Second)
	wantMax := after.Add(5 * time.Minute).Add(1 * time.Second)
	if repo.rateLimitedAt.Before(wantMin) || repo.rateLimitedAt.After(wantMax) {
		t.Fatalf("rateLimitedAt = %v, want ~5min from now (window [%v,%v])",
			repo.rateLimitedAt, wantMin, wantMax)
	}
}

func TestUS023_OpsRetry_ClassifiesChatCompletionsAsOpenAI(t *testing.T) {
	cases := []struct {
		name string
		path string
		want opsRetryRequestType
	}{
		// Positive: any /chat/completions path should be classified as OpenAI
		// so that NewAPI / OpenAI accounts get routed through openAIGatewayService.
		{name: "openai_v1_chat_completions", path: "/v1/chat/completions", want: opsRetryTypeOpenAI},
		{name: "openai_chat_completions_no_v1", path: "/chat/completions", want: opsRetryTypeOpenAI},
		{name: "openai_chat_completions_uppercase", path: "/V1/Chat/Completions", want: opsRetryTypeOpenAI},

		// Regression-protection: existing classifications must not shift.
		{name: "openai_responses", path: "/v1/responses", want: opsRetryTypeOpenAI},
		{name: "gemini_v1beta", path: "/v1beta/models/x:generateContent", want: opsRetryTypeGeminiV1B},
		{name: "anthropic_messages", path: "/v1/messages", want: opsRetryTypeMessages},
		{name: "empty_path_defaults_to_messages", path: "", want: opsRetryTypeMessages},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectOpsRetryType(tc.path)
			if got != tc.want {
				t.Fatalf("detectOpsRetryType(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestUS023_OpsRetry_ExecuteWithAccount_GuardsOpenAICompatInMessagesDefault(t *testing.T) {
	// Defense-in-depth: even if a future refactor breaks classification and
	// routes an OpenAI-compat account through opsRetryTypeMessages-default,
	// the guard must fail fast with a descriptive error rather than silently
	// invoking gatewayService.Forward (Anthropic forwarder).
	svc := &OpsService{} // gatewayService nil — we expect to never reach it

	for _, platform := range []string{PlatformOpenAI, PlatformNewAPI} {
		t.Run(platform, func(t *testing.T) {
			account := &Account{ID: 9001, Platform: platform, ChannelType: 1}
			errorLog := &OpsErrorLogDetail{
				OpsErrorLog: OpsErrorLog{RequestPath: "/v1/messages"},
			}
			exec := svc.executeWithAccount(context.Background(), opsRetryTypeMessages, errorLog, []byte(`{}`), account)
			if exec == nil {
				t.Fatal("expected non-nil execution result")
			}
			if exec.status != opsRetryStatusFailed {
				t.Fatalf("status = %q, want %q", exec.status, opsRetryStatusFailed)
			}
			// Error message must name the platform AND mention OpenAI-compat / opsRetryTypeOpenAI
			// so operators can debug. Avoid asserting exact string to allow message tweaks.
			msg := exec.errorMessage
			if msg == "" {
				t.Fatal("expected non-empty errorMessage")
			}
			if !strings.Contains(msg, platform) {
				t.Fatalf("errorMessage = %q, want to mention platform %q", msg, platform)
			}
			if !strings.Contains(msg, "OpenAI-compat") && !strings.Contains(msg, "opsRetryTypeOpenAI") {
				t.Fatalf("errorMessage = %q, want to reference OpenAI-compat / opsRetryTypeOpenAI", msg)
			}
		})
	}
}

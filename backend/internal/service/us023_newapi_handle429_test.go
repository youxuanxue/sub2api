//go:build unit

package service

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// US-023 (round-2 audit) — 429 body-parse coverage for newapi.
//
// Round-1 audit (US-022) closed five admin-lifecycle gaps. Round-2 audit then
// found a silent fall-through bug at the runtime layer:
//
//	ratelimit_service.handle429: when upstream returns 429 without an
//	anthropic-ratelimit-unified-reset header, the body-parse switch only
//	handles PlatformOpenAI / PlatformGemini / PlatformAntigravity. NewAPI
//	falls into the "no reset time" branch and gets a default 5-minute lock
//	— the OpenAI-compat usage_limit_reached body (which new-api adaptors
//	forward verbatim) is silently ignored. This significantly under-counts
//	reset and causes immediate re-hammering on the same upstream key.
//
// The companion ops_retry tests previously in this file targeted
// `opsRetryTypeOpenAI` classification + executeWithAccount guarding. Upstream
// Wei-Shaw/sub2api commit 2eb622f2 retired the entire ops_retry/replay
// storage (file `ops_retry.go`, table `ops_retry_attempts`, columns
// `ops_error_logs.{request_body,request_headers,is_retryable,retry_count,…}`),
// so those tests are no longer applicable — the runtime path they guarded
// has been removed in favor of admin-side replay disabled by default.
// See docs/approved/newapi-followup-bugs-and-forwarding-fields.md US-023.

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

	expectedResetUnix := time.Now().Add(1 * time.Hour).Unix()
	body := []byte(`{"error":{"type":"usage_limit_reached","message":"limit reached","resets_at":` +
		strconv.FormatInt(expectedResetUnix, 10) + `}}`)
	headers := http.Header{}

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
	repo := &us023NewAPI429Repo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 4243, Platform: PlatformNewAPI, ChannelType: 1, Type: AccountTypeAPIKey}

	body := []byte(`{"error":{"type":"some_other_error","message":"oops"}}`)
	headers := http.Header{}

	before := time.Now()
	svc.handle429(context.Background(), account, headers, body)
	after := time.Now()

	if repo.rateLimitedCalls != 1 {
		t.Fatalf("expected SetRateLimited called once, got %d", repo.rateLimitedCalls)
	}
	wantMin := before.Add(5 * time.Second).Add(-1 * time.Second)
	wantMax := after.Add(5 * time.Second).Add(1 * time.Second)
	if repo.rateLimitedAt.Before(wantMin) || repo.rateLimitedAt.After(wantMax) {
		t.Fatalf("rateLimitedAt = %v, want ~5s from now (window [%v,%v])",
			repo.rateLimitedAt, wantMin, wantMax)
	}
}

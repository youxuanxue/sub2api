//go:build unit

package service

import (
	"net/http"
	"testing"
)

func TestTkIsAnthropicNonAuthoritative429(t *testing.T) {
	const rlBody = `{"type":"error","error":{"type":"rate_limit_error","message":"Error"}}`
	const envelopeBody = `{"type":"error","error":{"type":"rate_limit_error","message":"Upstream rate limit exceeded, please retry later"}}`
	const extraUsageBody = `{"type":"error","error":{"type":"rate_limit_error","message":"extra usage limit reached"}}`

	cases := []struct {
		name    string
		headers http.Header
		body    string
		want    bool
	}{
		{"header-less generic rate_limit_error => non-authoritative", http.Header{}, rlBody, true},
		{"header-less edge failover-exhausted envelope => non-authoritative", http.Header{}, envelopeBody, true},
		{"nil headers => non-authoritative", nil, rlBody, true},
		{
			"only Retry-After (TK envelopes set it too) => still non-authoritative",
			http.Header{"Retry-After": {"5"}},
			envelopeBody, true,
		},
		{
			"authoritative 5h-reset header => NOT non-authoritative",
			http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"9999999999"}},
			rlBody, false,
		},
		{
			"authoritative 7d-reset header => NOT non-authoritative",
			http.Header{"Anthropic-Ratelimit-Unified-7d-Reset": {"9999999999"}},
			rlBody, false,
		},
		{
			"other anthropic-ratelimit-* header (no reset) => NOT non-authoritative",
			http.Header{"Anthropic-Ratelimit-Requests-Remaining": {"0"}},
			rlBody, false,
		},
		{
			"extra-usage body (handled by its own skip) => NOT here",
			http.Header{},
			extraUsageBody, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tkIsAnthropicNonAuthoritative429(tc.headers, []byte(tc.body)); got != tc.want {
				t.Fatalf("tkIsAnthropicNonAuthoritative429 = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTkRetryableOnSameAccount(t *testing.T) {
	poolAnthropic := &Account{
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true},
	}
	nonPoolAnthropic := &Account{
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{},
	}
	const body = `{"type":"error","error":{"type":"rate_limit_error","message":"Error"}}`

	resp := func(status int, h http.Header) *http.Response {
		if h == nil {
			h = http.Header{}
		}
		return &http.Response{StatusCode: status, Header: h}
	}

	t.Run("pool_mode anthropic + header-less 429 => no same-account retry (switch immediately)", func(t *testing.T) {
		if tkRetryableOnSameAccount(poolAnthropic, resp(http.StatusTooManyRequests, nil), []byte(body)) {
			t.Fatal("expected false for non-authoritative 429")
		}
	})
	t.Run("pool_mode anthropic + authoritative 429 => same-account retry kept", func(t *testing.T) {
		h := http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"9999999999"}}
		if !tkRetryableOnSameAccount(poolAnthropic, resp(http.StatusTooManyRequests, h), []byte(body)) {
			t.Fatal("expected true for authoritative (header-ful) 429")
		}
	})
	t.Run("pool_mode anthropic + 503 (not 429) => unaffected, retry kept", func(t *testing.T) {
		if !tkRetryableOnSameAccount(poolAnthropic, resp(http.StatusServiceUnavailable, nil), []byte(body)) {
			t.Fatal("expected true for 503 (non-authoritative gate is 429-only)")
		}
	})
	t.Run("non-pool-mode => never same-account retry", func(t *testing.T) {
		if tkRetryableOnSameAccount(nonPoolAnthropic, resp(http.StatusTooManyRequests, nil), []byte(body)) {
			t.Fatal("expected false for non-pool-mode account")
		}
	})
	t.Run("nil account / resp => false", func(t *testing.T) {
		if tkRetryableOnSameAccount(nil, resp(429, nil), []byte(body)) {
			t.Fatal("expected false for nil account")
		}
		if tkRetryableOnSameAccount(poolAnthropic, nil, []byte(body)) {
			t.Fatal("expected false for nil resp")
		}
	})
}

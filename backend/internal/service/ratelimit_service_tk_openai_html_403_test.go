//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// TK regression for upstream Wei-Shaw/sub2api#2413 (partially_mitigated):
//
// The CF/Arkose keyword list in ratelimit_service.go catches Cloudflare's
// "Just a moment..." page and Arkose FunCaptcha, but the production sample
// pasted in the upstream issue is OpenAI's *own* access-denied HTML page —
// it contains none of `cloudflare` / `arkoselabs` / `funcaptcha` /
// `challenge-platform` / `just a moment`. Before this fix, that HTML body
// silently incremented the per-account 403 counter and wrote a 10-minute
// temp_unschedulable cooldown on a healthy OAuth account on the FIRST hit,
// then permanently SetError on the 3rd hit within 180 minutes. Result:
// healthy accounts evicted from the pool, all non-image traffic on those
// accounts denied too.
//
// Shape-based invariant (documented at ratelimit_service.go:128): a real
// OpenAI account-level 403 returns structured JSON. Any HTML body on a 403
// is upstream infrastructure rejecting this *request*, not OpenAI rejecting
// this *account* — must skip the counter and the cooldown.

// openAIAccessDeniedHTMLSample reproduces the exact HTML head shape pasted
// by the upstream reporter at issue #2413 (Cherry Studio / gpt-image-2
// triggering OpenAI 403). The distinguishing marker is OpenAI's brand
// `.logo{color:#8e8ea0}` and the `scale-appear` animation class — none of
// which match the CF/Arkose keyword list.
const openAIAccessDeniedHTMLSample = `<html>
 <head>
 <meta name="viewport" content="width=device-width, initial-scale=1" />
 <style global>body{font-family:Arial,Helvetica,sans-serif}.container{align-items:center;display:flex;flex-direction:column;gap:2rem;height:100%;justify-content:center;width:100%}@keyframes enlarge-appear{0%{opacity:0;transform:scale(75%) rotate(-90deg)}to{opacity:1;transform:scale(100%) rotate(0deg)}}.logo{color:#8e8ea0}.scale-appear{animation:enlarge-appear .4s ease-out}@media (min-width:768px){.scale-appear{height:128px;width:128px}}</style>
 </head>
 <body>
 <div class="container">
 <div class="logo scale-appear">Access denied</div>
 </div>
 </body>
</html>`

func TestRateLimitService_HandleUpstreamError_OpenAI403OpenAIAccessDeniedHTMLSkipsCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)

	account := &Account{
		ID:       2413,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(openAIAccessDeniedHTMLSample),
	)

	require.True(t, shouldDisable, "shouldDisable must remain true so failover proceeds")
	require.Equal(t, 0, repo.tempCalls, "OpenAI access-denied HTML must not write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls, "OpenAI access-denied HTML must not SetError")
	require.Empty(t, counter.incrementIDs, "OpenAI access-denied HTML must not increment the 403 counter")
}

// Variants of the shape predicate: different HTML opening tags, whitespace,
// BOM prefix, mixed case — all must be treated as HTML.
func TestRateLimitService_HandleUpstreamError_OpenAI403HTMLShapeVariantsSkipCooldown(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{name: "doctype_prefix", body: []byte(`<!DOCTYPE html><html><body>nope</body></html>`)},
		{name: "doctype_mixed_case", body: []byte(`<!DocType HTML><HTML><BODY>nope</BODY></HTML>`)},
		{name: "head_only", body: []byte(`<head><title>403</title></head>`)},
		{name: "body_only", body: []byte(`<body>Access denied</body>`)},
		{name: "meta_only", body: []byte(`<meta charset="utf-8"><p>Forbidden</p>`)},
		{name: "style_only", body: []byte(`<style>body{color:red}</style>403`)},
		{name: "leading_whitespace", body: []byte("   \n\t<html><body>nope</body></html>")},
		{name: "leading_bom", body: []byte("\xef\xbb\xbf<html><body>nope</body></html>")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			counter := &openAI403CounterCacheStub{counts: []int64{1}}
			service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			service.SetOpenAI403CounterCache(counter)

			account := &Account{
				ID:       2413,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
			}

			shouldDisable := service.HandleUpstreamError(
				context.Background(),
				account,
				http.StatusForbidden,
				http.Header{},
				tc.body,
			)

			require.True(t, shouldDisable)
			require.Equal(t, 0, repo.tempCalls)
			require.Equal(t, 0, repo.setErrorCalls)
			require.Empty(t, counter.incrementIDs)
		})
	}
}

// Regression guard against the opposite mistake: a structured JSON 403
// describing a real account problem (suspended, lacks permissions, workspace
// deactivated, etc.) must still go through the normal counter +
// temp_unschedulable path. If shape detection bled into legitimate JSON
// errors, real account-health problems would be silently masked.
func TestRateLimitService_HandleUpstreamError_OpenAI403JSONBodyStillCoolsDown(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{
			name: "structured_error_envelope",
			body: []byte(`{"error":{"message":"You do not have permission to access this endpoint.","type":"forbidden","code":"account_disabled_auth_error"}}`),
		},
		{
			name: "plain_text_forbidden",
			body: []byte(`Forbidden`),
		},
		{
			name: "empty_body",
			body: []byte(``),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			counter := &openAI403CounterCacheStub{counts: []int64{1}}
			service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			service.SetOpenAI403CounterCache(counter)

			account := &Account{
				ID:       902,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
			}

			shouldDisable := service.HandleUpstreamError(
				context.Background(),
				account,
				http.StatusForbidden,
				http.Header{},
				tc.body,
			)

			require.True(t, shouldDisable)
			require.Equal(t, 1, repo.tempCalls, "real JSON / opaque 403 must still write temp_unschedulable")
			require.Equal(t, []int64{902}, counter.incrementIDs)
		})
	}
}

// Unit-level coverage of the shape predicate itself, kept separate from the
// integration-style HandleUpstreamError tests so a regression localises
// quickly to the predicate vs. the wiring.
func TestOpenAIIsHTMLBody(t *testing.T) {
	htmlCases := []struct {
		name string
		body string
	}{
		{"doctype", `<!DOCTYPE html><html></html>`},
		{"html_tag", `<html><body></body></html>`},
		{"head_only", `<head></head>`},
		{"body_only", `<body></body>`},
		{"meta_only", `<meta charset="utf-8">`},
		{"style_only", `<style>body{}</style>`},
		{"mixed_case", `<HTML></HTML>`},
		{"leading_ws", "   \n\t<html></html>"},
		{"bom_prefix", "\xef\xbb\xbf<html></html>"},
		{"issue_2413_sample", openAIAccessDeniedHTMLSample},
		{"large_body_marker_in_probe", "<html>" + strings.Repeat("x", openAIHTMLProbe)},
	}
	for _, tc := range htmlCases {
		t.Run("html/"+tc.name, func(t *testing.T) {
			require.True(t, openAIIsHTMLBody([]byte(tc.body)), "expected HTML detection for %q", tc.name)
		})
	}

	nonHTMLCases := []struct {
		name string
		body string
	}{
		{"empty", ``},
		{"whitespace_only", `   `},
		{"json_envelope", `{"error":{"message":"forbidden"}}`},
		{"plain_text", `Forbidden`},
		{"xml_no_html", `<?xml version="1.0"?><root><a/></root>`},
		{"json_string_with_lt", `"<html>"`},
		{"marker_after_probe_window", "<x>" + strings.Repeat(" ", openAIHTMLProbe+16) + "<html>"},
	}
	for _, tc := range nonHTMLCases {
		t.Run("non_html/"+tc.name, func(t *testing.T) {
			require.False(t, openAIIsHTMLBody([]byte(tc.body)), "expected non-HTML for %q", tc.name)
		})
	}
}

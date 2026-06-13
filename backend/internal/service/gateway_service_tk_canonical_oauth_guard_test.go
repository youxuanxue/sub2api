//go:build unit

package service

import (
	"errors"
	"net/http"
	"testing"
)

func TestCheckCanonicalIngressUA_AllowsEmpty(t *testing.T) {
	h := http.Header{}
	if err := checkCanonicalIngressUA(h); err != nil {
		t.Fatalf("empty UA must pass, got %v", err)
	}
}

func TestCheckCanonicalIngressUA_AllowsClaudeCLIVariants(t *testing.T) {
	cases := []string{
		"claude-cli/2.1.150 (external, cli)",
		"claude-cli/2.1.19 (external, sdk-cli)",
		"claude-cli/2.1.142 (external, sdk-cli)",
		"claude-code/0.5.0",
		// unknown UA must pass (default allow on the canonical path so future
		// real cc variants do not need a code change)
		"my-internal-test/1.0",
		"",
	}
	for _, ua := range cases {
		h := http.Header{}
		if ua != "" {
			h.Set("User-Agent", ua)
		}
		if err := checkCanonicalIngressUA(h); err != nil {
			t.Errorf("ua=%q must be allowed, got %v", ua, err)
		}
	}
}

func TestCheckCanonicalIngressUA_RejectsThirdPartySDKs(t *testing.T) {
	cases := []string{
		"OpenAI/Python 2.38.0",
		"openai-python/1.2.3",
		"httpx/0.27.0",
		"python-requests/2.31.0",
		"node-fetch/2.6.1",
		"axios/1.6.0",
		"got (https://github.com/sindresorhus/got)",
		"undici",
		"Go-http-client/2.0",
		"curl/8.4.0",
		"Wget/1.21.4",
		"PostmanRuntime/7.36.0",
		"insomnia/8.6.1",
		"libcurl/7.88.1",
		"okhttp/4.12.0",
		"Java/17.0.9",
		"reqwest/0.11.24",
		"aiohttp/3.9.1",
	}
	for _, ua := range cases {
		h := http.Header{}
		h.Set("User-Agent", ua)
		err := checkCanonicalIngressUA(h)
		if err == nil {
			t.Errorf("ua=%q must be rejected, got nil", ua)
			continue
		}
		var rej *CanonicalIngressUARejectedError
		if !errors.As(err, &rej) {
			t.Errorf("ua=%q expected *CanonicalIngressUARejectedError, got %T", ua, err)
		}
		if rej != nil && rej.IngressUA != ua {
			t.Errorf("ua=%q rejected but IngressUA=%q (lost original)", ua, rej.IngressUA)
		}
	}
}

// TestCheckCanonicalIngressUA_ShortPrefixesIntentionallyNotInDenyList documents
// the deliberate trade-off behind R-003: short generic tokens like "got/" and
// "requests/" are NOT in the deny-list because they would false-positive on
// any legitimate UA containing those substrings ("forgot/...", "*-requests/...").
// `python-requests/` and `got (` are the precise anchors that ARE pinned;
// anything looser stays off-list.
func TestCheckCanonicalIngressUA_ShortPrefixesIntentionallyNotInDenyList(t *testing.T) {
	cases := []string{
		"got/12.0.0",      // bare `got/` is too generic — let it pass
		"requests/2.31.0", // bare `requests/` is too generic — `python-requests/` covers the real case
	}
	for _, ua := range cases {
		h := http.Header{}
		h.Set("User-Agent", ua)
		if err := checkCanonicalIngressUA(h); err != nil {
			t.Errorf("ua=%q must NOT be rejected (short-prefix carve-out), got %v", ua, err)
		}
	}
}

// --- strict allow-list gate (SettingKeyAnthropicCanonicalIngressStrictEnabled) ---

func TestCheckCanonicalIngressUAStrict_RejectsEmpty(t *testing.T) {
	h := http.Header{}
	err := checkCanonicalIngressUAStrict(h)
	if err == nil {
		t.Fatalf("strict mode must reject empty UA, got nil")
	}
	var rej *CanonicalIngressUARejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("expected *CanonicalIngressUARejectedError, got %T", err)
	}
}

func TestCheckCanonicalIngressUAStrict_AllowsClaudeClients(t *testing.T) {
	cases := []string{
		"claude-cli/2.1.170 (external, cli)",
		"claude-cli/2.1.19 (external, sdk-cli)",
		"claude-code/1.2.3",
		// case-insensitive + leading whitespace must still pass
		" Claude-CLI/2.2",
		"  claude-code/0.5.0",
	}
	for _, ua := range cases {
		h := http.Header{}
		h.Set("User-Agent", ua)
		if err := checkCanonicalIngressUAStrict(h); err != nil {
			t.Errorf("strict mode must allow ua=%q, got %v", ua, err)
		}
	}
}

func TestCheckCanonicalIngressUAStrict_RejectsThirdPartyAndUnknown(t *testing.T) {
	cases := []string{
		"openai-python/1.0",
		"python-requests/2.31",
		"OpenAI/Python 2.38.0",
		"httpx/0.27.0",
		// unknown UAs that the deny-list would have ALLOWED must now be rejected
		"foo/1.0",
		"my-internal-test/1.0",
		"node-fetch/2.6.1",
		// spoof-prefix regression lock: the allow-list pins the trailing slash
		// ("claude-cli/" / "claude-code/"). If someone drops the slash to a bare
		// "claude-cli", these mimic UAs would silently pass — keep them rejected.
		"claude-cli-evil/2.2", // trailing char after "claude-cli" is "-", not "/"
		"claude-codex/1.0",    // "claude-code" + "x", not "claude-code/"
		"claude-cli",          // no slash, no version — bare token must not match
		"xclaude-cli/2.2",     // allowed prefix not anchored at start of UA
	}
	for _, ua := range cases {
		h := http.Header{}
		h.Set("User-Agent", ua)
		err := checkCanonicalIngressUAStrict(h)
		if err == nil {
			t.Errorf("strict mode must reject ua=%q, got nil", ua)
			continue
		}
		var rej *CanonicalIngressUARejectedError
		if !errors.As(err, &rej) {
			t.Errorf("ua=%q expected *CanonicalIngressUARejectedError, got %T", ua, err)
		}
	}
}

// TestCheckCanonicalIngressUA_DenyListUnchangedUnderStrictFeature documents the
// zero-regression invariant: the original deny-list function must keep allowing
// empty + unknown UAs regardless of the new strict gate's existence. Only the
// strict function changes behavior, and only when the setting opts in.
func TestCheckCanonicalIngressUA_DenyListUnchangedUnderStrictFeature(t *testing.T) {
	allowed := []string{"", "foo/1.0", "my-internal-test/1.0"}
	for _, ua := range allowed {
		h := http.Header{}
		if ua != "" {
			h.Set("User-Agent", ua)
		}
		if err := checkCanonicalIngressUA(h); err != nil {
			t.Errorf("deny-list (default path) must still allow ua=%q, got %v", ua, err)
		}
	}
}

// TestCountTokensSharesStrictUAGate documents D3: the count_tokens path reuses
// checkCanonicalIngressUAStrict (the same allow-list gate as /v1/messages), so a
// third-party / empty UA that would be rejected on messages is also rejected on
// count_tokens when strict mode is on. The gate function is the single source of
// truth; both call sites invoke it identically under canonical OAuth + strict.
func TestCountTokensSharesStrictUAGate(t *testing.T) {
	rejected := []string{"", "python-requests/2.31", "foo/1.0"}
	for _, ua := range rejected {
		h := http.Header{}
		if ua != "" {
			h.Set("User-Agent", ua)
		}
		if err := checkCanonicalIngressUAStrict(h); err == nil {
			t.Errorf("count_tokens strict gate must reject ua=%q (parity with messages)", ua)
		}
	}
	for _, ua := range []string{"claude-cli/2.1.170 (external, cli)", "claude-code/1.0.0"} {
		h := http.Header{}
		h.Set("User-Agent", ua)
		if err := checkCanonicalIngressUAStrict(h); err != nil {
			t.Errorf("count_tokens strict gate must allow legit cc ua=%q, got %v", ua, err)
		}
	}
}

// TestShouldRewriteSystemForNonCCMimicry covers the haiku mimicry gap (D2):
// default (strict=false) skips haiku rewrite (zero regression); strict=true
// rewrites haiku too so a non-CC haiku request gets the CC system/billing block.
func TestShouldRewriteSystemForNonCCMimicry(t *testing.T) {
	cases := []struct {
		model  string
		strict bool
		want   bool
	}{
		// default mode: haiku skips, everything else rewrites (current behavior)
		{"claude-haiku-4-5", false, false},
		{"claude-haiku-4-5-20251001", false, false},
		{"Claude-Haiku-4-5", false, false},
		{"claude-sonnet-4-5", false, true},
		{"claude-opus-4-7", false, true},
		{"", false, true},
		// strict mode: haiku is rewritten too (gap closed)
		{"claude-haiku-4-5", true, true},
		{"claude-haiku-4-5-20251001", true, true},
		{"claude-sonnet-4-5", true, true},
	}
	for _, c := range cases {
		got := shouldRewriteSystemForNonCCMimicry(c.model, c.strict)
		if got != c.want {
			t.Errorf("model=%q strict=%v: want %v got %v", c.model, c.strict, c.want, got)
		}
	}
}

func TestRemapDeprecatedOpusOnCanonical_RetiredOpusToCurrentDefault(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-6":          canonicalDefaultOpus,
		"claude-opus-4-6-20250930": canonicalDefaultOpus,
		"claude-opus-4-5":          canonicalDefaultOpus,
		"claude-opus-4-5-20250520": canonicalDefaultOpus,
		"claude-opus-4-4":          canonicalDefaultOpus,
		"claude-opus-4-1":          canonicalDefaultOpus,
		"claude-opus-4-0":          canonicalDefaultOpus,
		// case-insensitive
		"Claude-Opus-4-6": canonicalDefaultOpus,
	}
	for in, want := range cases {
		got, remapped := remapDeprecatedOpusOnCanonical(in)
		if !remapped {
			t.Errorf("model=%q must be remapped, remapped=false", in)
			continue
		}
		if got != want {
			t.Errorf("model=%q want %q got %q", in, want, got)
		}
	}
}

func TestRemapDeprecatedOpusOnCanonical_CurrentAndNonOpusUnchanged(t *testing.T) {
	cases := []string{
		"claude-opus-4-7",
		"claude-opus-4-7-20260301",
		"claude-sonnet-4-6",
		"claude-sonnet-4-5",
		"claude-haiku-4-5-20251001",
		"claude-haiku-4-5",
		"",
	}
	for _, in := range cases {
		got, remapped := remapDeprecatedOpusOnCanonical(in)
		if remapped {
			t.Errorf("model=%q must NOT be remapped, got %q remapped=true", in, got)
		}
		if got != in {
			t.Errorf("model=%q unchanged passthrough expected, got %q", in, got)
		}
	}
}

// TestRemapDeprecatedOpusOnCanonical_OpusPrefixIsolation verifies that the
// prefix check requires either an exact match or a "-" separator so
// "claude-opus-4-60" (hypothetical) is NOT treated as opus-4-6.
func TestRemapDeprecatedOpusOnCanonical_OpusPrefixIsolation(t *testing.T) {
	cases := []string{
		"claude-opus-4-60",
		"claude-opus-4-65",
		"claude-opus-4-7x",
	}
	for _, in := range cases {
		got, remapped := remapDeprecatedOpusOnCanonical(in)
		if remapped {
			t.Errorf("model=%q must NOT be remapped (prefix-isolation), got %q remapped=true", in, got)
		}
	}
}

// TestDeprecatedOpusRemapEligible_ScopedToAnthropicOAuthOrSetupToken asserts the
// broadened remap gate: ALL Anthropic OAuth/SetupToken accounts are eligible
// (not just canonical-TLS ones), while API-key and non-Anthropic accounts are not.
func TestDeprecatedOpusRemapEligible_ScopedToAnthropicOAuthOrSetupToken(t *testing.T) {
	cases := []struct {
		name    string
		account *Account
		want    bool
	}{
		{"anthropic_oauth", &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, true},
		{"anthropic_setup_token", &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}, true},
		{"anthropic_apikey", &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, false},
		{"openai_oauth", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, false},
		{"nil_account", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deprecatedOpusRemapEligible(tc.account); got != tc.want {
				t.Errorf("deprecatedOpusRemapEligible(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestDeprecatedOpusRemapEligible_BroaderThanCanonicalUAGate documents the
// intentional scope split: a non-canonical Anthropic OAuth account (one the
// canonical UA-reject gate would skip) is still eligible for the deprecated-opus
// remap. That is the whole point of broadening — retired opus must be upgraded on
// every OAuth path, not only the canonical-TLS one.
func TestDeprecatedOpusRemapEligible_BroaderThanCanonicalUAGate(t *testing.T) {
	acct := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	if !deprecatedOpusRemapEligible(acct) {
		t.Fatal("non-canonical Anthropic OAuth account must be remap-eligible")
	}
	if got, remapped := remapDeprecatedOpusOnCanonical("claude-opus-4-6"); !remapped || got != canonicalDefaultOpus {
		t.Errorf("retired opus must remap to %q, got %q remapped=%v", canonicalDefaultOpus, got, remapped)
	}
}

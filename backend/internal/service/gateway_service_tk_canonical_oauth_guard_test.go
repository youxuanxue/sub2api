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
		"requests/2.31.0",
		"node-fetch/2.6.1",
		"axios/1.6.0",
		"got (https://github.com/sindresorhus/got)",
		"got/12.0.0",
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

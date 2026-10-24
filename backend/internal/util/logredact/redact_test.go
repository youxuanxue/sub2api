package logredact

import (
	"strings"
	"testing"
)

func TestRedactText_JSONLike(t *testing.T) {
	in := `{"access_token":"ya29.a0AfH6SMDUMMY","refresh_token":"1//0gDUMMY","other":"ok"}`
	out := RedactText(in)
	if out == in {
		t.Fatalf("expected redaction, got unchanged")
	}
	if want := `"access_token":"***"`; !strings.Contains(out, want) {
		t.Fatalf("expected %q in %q", want, out)
	}
	if want := `"refresh_token":"***"`; !strings.Contains(out, want) {
		t.Fatalf("expected %q in %q", want, out)
	}
}

func TestRedactText_AuthorizationTails(t *testing.T) {
	cases := []string{
		`authorization=Digest username="a,b", response="QA_SECRET"`,
		`authorization=Digest username="public",,response="QA_SECRET"`,
		`headers={'authorization': 'Basic QA_SECRET'}`,
		`{"authorization": Basic QA_SECRET, retrying upstream request}`,
		`authorization=Bearer abc QA_SECRET, status=ok`,
		`authorization=Bearer [REDACTED] QA_SECRET`,
	}
	for _, in := range cases {
		out := RedactText(in)
		if strings.Contains(out, "QA_SECRET") {
			t.Fatalf("credential tail leaked from %q: %q", in, out)
		}
	}
}

func TestRedactText_TrailingQuoteOnBareAuthorizationKeyIsNotQuotedKey(t *testing.T) {
	// Fuzz seed: a bare key with a stuck trailing quote must not activate the
	// quoted-authorization path and erase an innocent value.
	in := `AuthoriZAtion":0`
	if out := RedactText(in); out != in {
		t.Fatalf("expected bare key with trailing quote unchanged, got %q", out)
	}
}

func TestRedactText_QuotedAuthorizationNumericValueLeftAlone(t *testing.T) {
	// Fuzz seed: JSON-like numeric values are not auth schemes; keep parity
	// with the reference pipeline (do not wipe to ***).
	in := `"AuthoriZAtion":0`
	if out := RedactText(in); out != in {
		t.Fatalf("expected quoted key with numeric value unchanged, got %q", out)
	}
	// Stray leading quote before a bare key still redacts like the reference.
	if out := RedactText(`"AUTHORIZATION:0`); out != `"AUTHORIZATION:***` {
		t.Fatalf("expected bare key after stray quote to redact, got %q", out)
	}
}

func TestRedactText_QueryLike(t *testing.T) {
	in := "access_token=ya29.a0AfH6SMDUMMY refresh_token=1//0gDUMMY"
	out := RedactText(in)
	if strings.Contains(out, "ya29") || strings.Contains(out, "1//0") {
		t.Fatalf("expected tokens redacted, got %q", out)
	}
}

func TestRedactText_GOCSPX(t *testing.T) {
	in := "client_secret=GOCSPX-your-client-secret"
	out := RedactText(in)
	if strings.Contains(out, "your-client-secret") {
		t.Fatalf("expected secret redacted, got %q", out)
	}
	if !strings.Contains(out, "client_secret=***") {
		t.Fatalf("expected key redacted, got %q", out)
	}
}

func TestRedactText_GeminiAPIKeys(t *testing.T) {
	legacyKey := "AI" + "za" + strings.Repeat("Ab1_-", 7)
	authKey := "AQ." + "Ab" + strings.Repeat("Xy9_-z", 8)
	in := "upstream rejected " + legacyKey + " and " + authKey + " for model gemini-3.1-pro"
	out := RedactText(in)
	if strings.Contains(out, legacyKey) || strings.Contains(out, authKey) {
		t.Fatalf("expected Gemini keys redacted, got %q", out)
	}
	for _, want := range []string{"AIza***", "AQ.***", "for model gemini-3.1-pro"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
}

func TestRedactText_GeminiAuthKeyPrefixAloneUnchanged(t *testing.T) {
	in := "key format AQ.Ab... is documented"
	if out := RedactText(in); out != in {
		t.Fatalf("expected short AQ. prefix unchanged, got %q", out)
	}
}

func TestRedactText_ExtraKeyCacheUsesNormalizedSortedKey(t *testing.T) {
	clearExtraTextPatternCache()

	out1 := RedactText("custom_secret=abc", "Custom_Secret", " custom_secret ")
	out2 := RedactText("custom_secret=xyz", "custom_secret")
	if !strings.Contains(out1, "custom_secret=***") {
		t.Fatalf("expected custom key redacted in first call, got %q", out1)
	}
	if !strings.Contains(out2, "custom_secret=***") {
		t.Fatalf("expected custom key redacted in second call, got %q", out2)
	}

	if got := countExtraTextPatternCacheEntries(); got != 1 {
		t.Fatalf("expected 1 cached pattern set, got %d", got)
	}
}

func TestRedactText_DefaultPathDoesNotUseExtraCache(t *testing.T) {
	clearExtraTextPatternCache()

	out := RedactText("access_token=abc")
	if !strings.Contains(out, "access_token=***") {
		t.Fatalf("expected default key redacted, got %q", out)
	}
	if got := countExtraTextPatternCacheEntries(); got != 0 {
		t.Fatalf("expected extra cache to remain empty, got %d", got)
	}
}

func TestRedactText_ExtraKeyCacheSeparatesKeyLists(t *testing.T) {
	for _, lists := range [][][]string{
		{{"alpha,beta"}, {"alpha", "beta"}},
		{{"alpha", "beta"}, {"alpha,beta"}},
	} {
		clearExtraTextPatternCache()
		for _, keys := range lists {
			for _, key := range keys {
				if got, want := RedactText(key+"=synthetic", keys...), key+"=***"; got != want {
					t.Fatalf("keys %q, key %q: got %q, want %q", keys, key, got, want)
				}
			}
			if got := RedactText("unrelated=ordinary", keys...); got != "unrelated=ordinary" {
				t.Fatalf("unrelated key redacted with keys %q: %q", keys, got)
			}
			if len(keys) == 1 {
				if got := RedactText("alpha=ordinary", keys...); got != "alpha=ordinary" {
					t.Fatalf("separate key redacted with keys %q: %q", keys, got)
				}
			}
		}
	}
}

func TestRedactJSON_CredentialKeys(t *testing.T) {
	in := []byte(`{"authorization":"Bearer sk-live","x-api-key":"tk-key","cookie":"sid=abc","tool":{"name":"web_search","arguments":{"query":"token pricing","api_key":"secret"}},"usage":{"prompt_tokens":12,"completion_tokens":3}}`)
	out := RedactJSON(in)

	for _, leaked := range []string{"sk-live", "tk-key", "sid=abc", "secret"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("expected %q redacted, got %q", leaked, out)
		}
	}
	for _, preserved := range []string{"web_search", "token pricing", `"prompt_tokens":12`, `"completion_tokens":3`} {
		if !strings.Contains(out, preserved) {
			t.Fatalf("expected %q preserved, got %q", preserved, out)
		}
	}
}

func TestRedactJSON_SuffixKeys(t *testing.T) {
	out := RedactJSON([]byte(`{"stripe_signature":"sig-value","customer_password":"pw-value","max_tokens":1024}`))
	if strings.Contains(out, "sig-value") || strings.Contains(out, "pw-value") {
		t.Fatalf("expected suffix credential keys redacted, got %q", out)
	}
	if !strings.Contains(out, `"max_tokens":1024`) {
		t.Fatalf("expected max_tokens preserved, got %q", out)
	}
}

func clearExtraTextPatternCache() {
	extraTextPatternCache.Range(func(key, value any) bool {
		extraTextPatternCache.Delete(key)
		return true
	})
}

func countExtraTextPatternCacheEntries() int {
	count := 0
	extraTextPatternCache.Range(func(key, value any) bool {
		count++
		return true
	})
	return count
}

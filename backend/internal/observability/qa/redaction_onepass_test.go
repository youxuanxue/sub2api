package qa

import (
	"testing"
)

func TestSanitizeQABodyMemoReusesSameDigestAndOptions(t *testing.T) {
	svc := &Service{bodyMaxBytes: 1 << 20, optInBodyMaxBytes: 1 << 20}
	memo := newQARedactionMemo()
	body := []byte(`{"token":"secret","text":"ordinary"}`)
	first := svc.sanitizeQABodyMemo(body, false, memo)
	second := svc.sanitizeQABodyMemo(body, false, memo)
	if len(memo.values) != 1 {
		t.Fatalf("memo entries = %d, want 1", len(memo.values))
	}
	if _, ok := first.(map[string]any); !ok {
		t.Fatalf("memoized JSON result has unexpected type: %T", first)
	}
	if got := redactionValueString(second); got == "" || got == string(body) {
		t.Fatalf("memoized result was not redacted: %q", got)
	}
}

func TestSanitizeQABodyMemoUnknownFormatPassthrough(t *testing.T) {
	svc := &Service{bodyMaxBytes: 1 << 20, optInBodyMaxBytes: 1 << 20}
	got := svc.sanitizeQABodyMemo([]byte("opaque token=secret"), false, newQARedactionMemo())
	if got != "opaque token=secret" {
		t.Fatalf("unknown format = %#v, want original text", got)
	}
}

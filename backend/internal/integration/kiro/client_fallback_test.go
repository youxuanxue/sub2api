package kiro

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// recordingDoer returns 403 for the runtime.kiro.dev gateway and 200 (empty event
// stream) for any legacy amazonaws host, recording every host it is asked to hit.
type recordingDoer struct{ seen []string }

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	d.seen = append(d.seen, req.URL.Host)
	if strings.Contains(req.URL.Host, "kiro.dev") {
		return &http.Response{
			StatusCode: 403,
			Body:       io.NopCloser(strings.NewReader(`{"__type":"AccessDeniedException"}`)),
			Header:     http.Header{},
		}, nil
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
	}, nil
}

// A 403 from the runtime.kiro.dev gateway must NOT fail the request — it must fall
// through to the legacy amazonaws hosts (the migration's self-heal guarantee). This
// is the regression test for the auth-error short-circuit that, before the fix,
// returned immediately and would have surfaced a runtime-only 403 to the customer.
func TestCallKiroAPI_Runtime403FallsThroughToLegacy(t *testing.T) {
	doer := &recordingDoer{}
	account := &Account{AccessToken: "tok", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/x"}
	if err := CallKiroAPIWithDoer(doer, account, &KiroPayload{}, nil); err != nil {
		t.Fatalf("runtime 403 should fall through to a legacy 200, got error: %v", err)
	}
	if len(doer.seen) < 2 {
		t.Fatalf("expected fall-through to legacy, doer only saw: %v", doer.seen)
	}
	if !strings.Contains(doer.seen[0], "kiro.dev") {
		t.Fatalf("runtime.kiro.dev must be tried first, saw: %v", doer.seen)
	}
	var hitLegacy bool
	for _, h := range doer.seen[1:] {
		if strings.Contains(h, "amazonaws.com") {
			hitLegacy = true
		}
	}
	if !hitLegacy {
		t.Fatalf("a runtime 403 must fall through to a legacy amazonaws host, saw: %v", doer.seen)
	}
}

// A 403 from a legacy amazonaws host still short-circuits (same token would be
// rejected on the sibling amazonaws hosts) — i.e. the fall-through is scoped to the
// cross-gateway runtime.kiro.dev case, not a blanket retry of auth errors.
func TestCallKiroAPI_LegacyAuthErrorStillShortCircuits(t *testing.T) {
	doer := &legacy403Doer{}
	account := &Account{AccessToken: "tok", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/x"}
	err := CallKiroAPIWithDoer(doer, account, &KiroPayload{}, nil)
	if err == nil {
		t.Fatal("a legacy-host 403 should short-circuit and return an error")
	}
	// runtime (kiro.dev) tried first then exactly one legacy host before returning.
	if len(doer.seen) != 2 {
		t.Fatalf("expected runtime + one legacy host then short-circuit, saw: %v", doer.seen)
	}
}

type legacy403Doer struct{ seen []string }

func (d *legacy403Doer) Do(req *http.Request) (*http.Response, error) {
	d.seen = append(d.seen, req.URL.Host)
	// runtime falls through; the first legacy host 403s and must short-circuit.
	code := 200
	if strings.Contains(req.URL.Host, "kiro.dev") || strings.Contains(req.URL.Host, "amazonaws.com") {
		code = 403
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(`{"__type":"AccessDeniedException"}`)),
		Header:     http.Header{},
	}, nil
}

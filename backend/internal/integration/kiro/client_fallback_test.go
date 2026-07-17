package kiro

import (
	"context"
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

type readFailureThenSuccessDoer struct {
	seenContextValue any
	calls            int
}

type requestMarkerContextKey struct{}

func (d *readFailureThenSuccessDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	d.seenContextValue = req.Context().Value(requestMarkerContextKey{})
	body := ""
	if d.calls == 1 {
		body = "\x00\x00\x00\x14"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

func TestCallKiroAPI_ResponseReadFailureFallsThroughWhenConsumerCanReset(t *testing.T) {
	doer := &readFailureThenSuccessDoer{}
	account := &Account{AccessToken: "tok", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/x"}
	resetCalls := 0
	callback := &KiroStreamCallback{ResetForRetry: func() bool {
		resetCalls++
		return true
	}}
	ctx := context.WithValue(context.Background(), requestMarkerContextKey{}, "customer-request")

	if err := CallKiroAPIWithDoerContext(ctx, doer, account, &KiroPayload{}, callback); err != nil {
		t.Fatalf("read failure should fall through to the next endpoint: %v", err)
	}
	if doer.calls != 2 || resetCalls != 1 {
		t.Fatalf("calls=%d resetCalls=%d, want 2 and 1", doer.calls, resetCalls)
	}
	if doer.seenContextValue != "customer-request" {
		t.Fatalf("upstream request did not inherit caller context: %v", doer.seenContextValue)
	}
}

func TestCallKiroAPI_ResponseReadFailureDoesNotReplayCommittedConsumer(t *testing.T) {
	doer := &readFailureThenSuccessDoer{}
	account := &Account{AccessToken: "tok", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/x"}
	callback := &KiroStreamCallback{ResetForRetry: func() bool { return false }}

	err := CallKiroAPIWithDoerContext(context.Background(), doer, account, &KiroPayload{}, callback)
	if err == nil || doer.calls != 1 {
		t.Fatalf("committed consumer must not be replayed: calls=%d err=%v", doer.calls, err)
	}
}

func TestCallKiroAPI_CanceledContextSkipsEndpointAttempts(t *testing.T) {
	doer := &readFailureThenSuccessDoer{}
	account := &Account{AccessToken: "tok", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/x"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CallKiroAPIWithDoerContext(ctx, doer, account, &KiroPayload{}, nil)
	if err != context.Canceled || doer.calls != 0 {
		t.Fatalf("canceled request should stop before dialing: calls=%d err=%v", doer.calls, err)
	}
}

// A 403 from the runtime.kiro.dev gateway must not fail the request immediately;
// it may fall through to the officially transitional q host. This
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

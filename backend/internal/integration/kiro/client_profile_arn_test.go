package kiro

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type staleProfileChatDoer struct {
	calls atomic.Int32
}

func (d *staleProfileChatDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls.Add(1)
	body, _ := io.ReadAll(req.Body)
	var payload struct {
		ProfileArn string `json:"profileArn"`
	}
	_ = json.Unmarshal(body, &payload)
	if strings.Contains(payload.ProfileArn, "profile/stale") {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"message":"Invalid profileArn."}`)),
			Header:     http.Header{},
		}, nil
	}
	if strings.TrimSpace(payload.ProfileArn) == "" {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(strings.NewReader(`{"message":"profileArn is required for this request."}`)),
			Header:     http.Header{},
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     http.Header{},
	}, nil
}

// restProxyDoer forwards control-plane REST calls to srv while delegating chat
// traffic to inner. Mirrors edge TLS doer usage where profileArn resolution and
// chat share the same transport.
type restProxyDoer struct {
	restHost string
	inner    HTTPDoer
}

func (d *restProxyDoer) Do(req *http.Request) (*http.Response, error) {
	if req.URL != nil && (strings.Contains(req.URL.Path, "ListAvailableProfiles") ||
		strings.Contains(req.URL.Path, "getUsageLimits") ||
		strings.Contains(req.URL.Host, "management.") ||
		strings.Contains(req.URL.Host, "codewhisperer.")) {
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = "http"
		cloned.URL.Host = d.restHost
		return http.DefaultClient.Do(cloned)
	}
	if d.inner != nil {
		return d.inner.Do(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestCallKiroAPI_RetriesAfterStaleProfileArn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "ListAvailableProfiles") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]string{
				{"arn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/fresh"},
			},
		})
	}))
	defer srv.Close()

	chat := &staleProfileChatDoer{}
	doer := &restProxyDoer{restHost: srv.Listener.Addr().String(), inner: chat}
	account := &Account{
		AccessToken:  "tok",
		RefreshToken: "rt",
		ProfileArn:   "arn:aws:codewhisperer:us-east-1:123456789012:profile/stale",
	}
	if err := CallKiroAPIWithDoer(doer, account, &KiroPayload{}, nil); err != nil {
		t.Fatalf("CallKiroAPIWithDoer() error = %v", err)
	}
	if chat.calls.Load() < 2 {
		t.Fatalf("chat doer calls = %d, want at least 2 (stale pass then fresh retry)", chat.calls.Load())
	}
	if got := account.ProfileArn; got != "arn:aws:codewhisperer:us-east-1:123456789012:profile/fresh" {
		t.Fatalf("account.ProfileArn = %q, want fresh ARN", got)
	}
}

func TestCallKiroAPI_ResolvesMissingProfileArnViaInjectedDoer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "ListAvailableProfiles") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"profiles": []map[string]string{
				{"arn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/fresh"},
			},
		})
	}))
	defer srv.Close()

	chat := &staleProfileChatDoer{}
	doer := &restProxyDoer{restHost: srv.Listener.Addr().String(), inner: chat}
	account := &Account{AccessToken: "tok", RefreshToken: "rt"}
	if err := CallKiroAPIWithDoer(doer, account, &KiroPayload{}, nil); err != nil {
		t.Fatalf("CallKiroAPIWithDoer() error = %v", err)
	}
	if chat.calls.Load() != 1 {
		t.Fatalf("chat doer calls = %d, want 1", chat.calls.Load())
	}
	if got := account.ProfileArn; got != "arn:aws:codewhisperer:us-east-1:123456789012:profile/fresh" {
		t.Fatalf("account.ProfileArn = %q, want resolved ARN", got)
	}
}

func TestCallKiroAPI_FailsWhenProfileArnUnresolved(t *testing.T) {
	account := &Account{AccessToken: "tok"}
	err := CallKiroAPIWithDoer(&staleProfileChatDoer{}, account, &KiroPayload{}, nil)
	if err == nil {
		t.Fatal("CallKiroAPIWithDoer() expected error when profileArn cannot be resolved")
	}
	if !strings.Contains(err.Error(), "resolve profileArn") && !strings.Contains(err.Error(), "no available Kiro profile") {
		t.Fatalf("error = %q, want profileArn resolution failure", err)
	}
}

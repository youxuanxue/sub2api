package kiro

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type rewriteHostTransport struct {
	target string
	base   http.RoundTripper
}

func (t *rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = "http"
	cloned.URL.Host = t.target
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

func installTestRestClient(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prev := kiroRestHttpStore.Load()
	kiroRestHttpStore.Store(&http.Client{
		Transport: &rewriteHostTransport{target: srv.Listener.Addr().String()},
	})
	t.Cleanup(func() { kiroRestHttpStore.Store(prev) })
}

func TestGetUsageLimits_AutoResolvesMissingProfileArn(t *testing.T) {
	var listCalls atomic.Int32
	var usageCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "ListAvailableProfiles"):
			listCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"profiles": []map[string]string{
					{"arn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/good"},
				},
			})
		case strings.Contains(r.URL.Path, "getUsageLimits"):
			usageCalls.Add(1)
			if !strings.Contains(r.URL.RawQuery, "profileArn=") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"message":"Invalid profileArn."}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"usageBreakdownList": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	installTestRestClient(t, srv)

	account := &Account{AccessToken: "tok", RefreshToken: "rt"}
	resp, err := GetUsageLimits(account)
	if err != nil {
		t.Fatalf("GetUsageLimits() error = %v", err)
	}
	if resp == nil {
		t.Fatal("GetUsageLimits() returned nil response")
	}
	if listCalls.Load() != 1 {
		t.Fatalf("ListAvailableProfiles calls = %d, want 1", listCalls.Load())
	}
	if usageCalls.Load() == 0 {
		t.Fatal("expected getUsageLimits to be called")
	}
	if got := account.ProfileArn; got != "arn:aws:codewhisperer:us-east-1:123456789012:profile/good" {
		t.Fatalf("account.ProfileArn = %q, want resolved ARN", got)
	}
}

func TestGetUsageLimits_RetriesAfterStaleProfileArn(t *testing.T) {
	var listCalls atomic.Int32
	var usageCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "ListAvailableProfiles"):
			listCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"profiles": []map[string]string{
					{"arn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/fresh"},
				},
			})
		case strings.Contains(r.URL.Path, "getUsageLimits"):
			usageCalls.Add(1)
			if strings.Contains(r.URL.RawQuery, "profile%2Fstale") || strings.Contains(r.URL.RawQuery, "profile/stale") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"message":"Invalid profileArn."}`)
				return
			}
			if !strings.Contains(r.URL.RawQuery, "profile%2Ffresh") && !strings.Contains(r.URL.RawQuery, "profile/fresh") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"message":"Invalid profileArn."}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"usageBreakdownList": []any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	installTestRestClient(t, srv)

	account := &Account{
		AccessToken:  "tok",
		RefreshToken: "rt",
		ProfileArn:   "arn:aws:codewhisperer:us-east-1:123456789012:profile/stale",
	}
	resp, err := GetUsageLimits(account)
	if err != nil {
		t.Fatalf("GetUsageLimits() error = %v", err)
	}
	if resp == nil {
		t.Fatal("GetUsageLimits() returned nil response")
	}
	if listCalls.Load() != 1 {
		t.Fatalf("ListAvailableProfiles calls = %d, want 1 after stale retry", listCalls.Load())
	}
	if usageCalls.Load() < 2 {
		t.Fatalf("getUsageLimits calls = %d, want at least 2 (stale then fresh)", usageCalls.Load())
	}
	if got := account.ProfileArn; got != "arn:aws:codewhisperer:us-east-1:123456789012:profile/fresh" {
		t.Fatalf("account.ProfileArn = %q, want fresh ARN", got)
	}
}

func TestIsInvalidProfileArnError(t *testing.T) {
	if !isInvalidProfileArnError(fmt.Errorf(`HTTP 400 from https://codewhisperer.us-east-1.amazonaws.com: {"message":"Invalid profileArn."}`)) {
		t.Fatal("expected invalid profileArn detection")
	}
	if !isInvalidProfileArnError(fmt.Errorf(`HTTP 400 from https://runtime.us-east-1.kiro.dev: {"message":"profileArn is required for this request."}`)) {
		t.Fatal("expected missing profileArn detection")
	}
	if isInvalidProfileArnError(fmt.Errorf("HTTP 401: unauthorized")) {
		t.Fatal("401 should not match invalid profileArn")
	}
}

func TestRefreshAccountInfo_MapsBonusesFromUsageBreakdown(t *testing.T) {
	var usageCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "getUsageLimits"):
			usageCalls.Add(1)
			_, _ = io.WriteString(w, `{
				"usageBreakdownList": [{
					"resourceType": "AGENTIC_REQUEST",
					"currentUsage": 300,
					"usageLimit": 1000,
					"bonuses": [{
						"bonusCode": "WELCOME500",
						"displayName": "Welcome Bonus",
						"currentUsage": 120,
						"usageLimit": 500,
						"status": "ACTIVE",
						"expiresAt": 1893456000
					}]
				}],
				"nextDateReset": 1893456000,
				"subscriptionInfo": {"subscriptionTitle": "KIRO POWER"}
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	installTestRestClient(t, srv)

	account := &Account{
		AccessToken: "tok",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:1:profile/test",
	}
	info, err := RefreshAccountInfo(account)
	if err != nil {
		t.Fatalf("RefreshAccountInfo() error = %v", err)
	}
	if usageCalls.Load() != 1 {
		t.Fatalf("getUsageLimits calls = %d, want 1", usageCalls.Load())
	}
	if len(info.Bonuses) != 1 {
		t.Fatalf("bonuses = %+v, want one entry", info.Bonuses)
	}
	if info.Bonuses[0].Code != "WELCOME500" || info.Bonuses[0].Limit != 500 {
		t.Fatalf("bonus = %+v", info.Bonuses[0])
	}
	if info.Bonuses[0].Percent != 24 {
		t.Fatalf("bonus percent = %v, want 24", info.Bonuses[0].Percent)
	}
}

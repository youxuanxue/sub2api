package kiro

import (
	"encoding/json"
	"errors"
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

type restDoerFunc func(*http.Request) (*http.Response, error)

func (f restDoerFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func restTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
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
		case r.URL.Path == "/Get-Usage-Limits" || r.URL.Path == "/getUsageLimits":
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
		case r.URL.Path == "/Get-Usage-Limits" || r.URL.Path == "/getUsageLimits":
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

func TestGetUsageLimits_UsesHostSpecificPathsAndLegacyFallback(t *testing.T) {
	account := &Account{
		AccessToken: "tok",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:1:profile/test",
	}
	var seen []string
	doer := restDoerFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.URL.Host+req.URL.Path)
		switch req.URL.Host {
		case "management.us-east-1.kiro.dev":
			if req.URL.Path != "/Get-Usage-Limits" {
				t.Fatalf("management path = %q, want /Get-Usage-Limits", req.URL.Path)
			}
			return restTestResponse(http.StatusForbidden, `{"message":"management unavailable"}`), nil
		case "codewhisperer.us-east-1.amazonaws.com":
			if req.URL.Path != "/getUsageLimits" {
				t.Fatalf("legacy path = %q, want /getUsageLimits", req.URL.Path)
			}
			return restTestResponse(http.StatusOK, `{"usageBreakdownList":[]}`), nil
		default:
			t.Fatalf("unexpected host %q", req.URL.Host)
			return nil, nil
		}
	})

	if _, err := getUsageLimitsWithDoer(account, doer); err != nil {
		t.Fatalf("getUsageLimitsWithDoer() error = %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("attempts = %v, want management then legacy", seen)
	}
}

func TestGetUsageLimits_PrimaryQuotaErrorNotMaskedByLegacyInvalidToken(t *testing.T) {
	account := &Account{
		AccessToken: "tok",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:1:profile/test",
	}
	var calls int
	doer := restDoerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host == "management.us-east-1.kiro.dev" {
			return restTestResponse(http.StatusPaymentRequired, `{"message":"You have reached the limit."}`), nil
		}
		return restTestResponse(http.StatusForbidden, `{"message":"Invalid token"}`), nil
	})

	_, err := getUsageLimitsWithDoer(account, doer)
	if err == nil {
		t.Fatal("getUsageLimitsWithDoer() expected error")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if got := err.Error(); !strings.Contains(got, "HTTP 402") || !strings.Contains(got, "reached the limit") {
		t.Fatalf("error = %q, want primary management quota verdict", got)
	} else if strings.Contains(got, "Invalid token") || strings.Contains(got, "HTTP 403") {
		t.Fatalf("error = %q, legacy auth error must not mask primary verdict", got)
	}
}

func TestGetUsageLimits_PrimaryQuotaErrorNotReclassifiedAsStaleProfile(t *testing.T) {
	account := &Account{
		AccessToken: "tok",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:1:profile/test",
	}
	var calls int
	doer := restDoerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/Get-Usage-Limits" && req.URL.Path != "/getUsageLimits" {
			t.Fatalf("unexpected stale-profile recovery request: %s%s", req.URL.Host, req.URL.Path)
		}
		if req.URL.Host == "management.us-east-1.kiro.dev" {
			return restTestResponse(http.StatusPaymentRequired, `{"message":"You have reached the limit."}`), nil
		}
		return restTestResponse(http.StatusBadRequest, `{"message":"Invalid profileArn."}`), nil
	})

	_, err := getUsageLimitsWithDoer(account, doer)
	if err == nil {
		t.Fatal("getUsageLimitsWithDoer() expected error")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want only management and legacy usage attempts", calls)
	}
	if got := err.Error(); !strings.Contains(got, "HTTP 402") || !strings.Contains(got, "reached the limit") {
		t.Fatalf("error = %q, want primary management quota verdict", got)
	}
}

func TestGetUsageLimits_AllHostsInvalidTokenKeepsAuthVerdict(t *testing.T) {
	account := &Account{
		AccessToken: "tok",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:1:profile/test",
	}
	var calls int
	doer := restDoerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		body := `{"message":"Invalid token on legacy"}`
		if req.URL.Host == "management.us-east-1.kiro.dev" {
			body = `{"message":"Invalid token"}`
		}
		return restTestResponse(http.StatusForbidden, body), nil
	})

	_, err := getUsageLimitsWithDoer(account, doer)
	if err == nil {
		t.Fatal("getUsageLimitsWithDoer() expected error")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if got := err.Error(); !strings.Contains(got, "HTTP 403") || !strings.Contains(got, `"message":"Invalid token"`) {
		t.Fatalf("error = %q, want primary Invalid token verdict", got)
	} else if strings.Contains(got, "on legacy") {
		t.Fatalf("error = %q, want primary management verdict", got)
	}
}

func TestGetUsageLimits_LegacyHTTPVerdictAfterManagementTransportFailure(t *testing.T) {
	account := &Account{
		AccessToken: "tok",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:1:profile/test",
	}
	doer := restDoerFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "management.us-east-1.kiro.dev" {
			return nil, errors.New("management transport failed")
		}
		return restTestResponse(http.StatusForbidden, `{"message":"Invalid token on legacy"}`), nil
	})

	_, err := getUsageLimitsWithDoer(account, doer)
	if err == nil {
		t.Fatal("getUsageLimitsWithDoer() expected error")
	}
	if got := err.Error(); !strings.Contains(got, "HTTP 403 from "+kiroRestAPIBase) || !strings.Contains(got, "on legacy") {
		t.Fatalf("error = %q, want legacy HTTP verdict after management transport failure", got)
	}
}

func TestKiroRestFetch_GenericOperationsKeepLastHTTPError(t *testing.T) {
	account := &Account{AccessToken: "tok"}
	doer := restDoerFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "management.us-east-1.kiro.dev" {
			return restTestResponse(http.StatusNotFound, `{"message":"unsupported on management"}`), nil
		}
		return restTestResponse(http.StatusForbidden, `{"message":"legacy auth verdict"}`), nil
	})

	_, err := kiroRestFetchWithDoer(account, http.MethodGet, "/ListAvailableModels", "", false, doer)
	if err == nil {
		t.Fatal("kiroRestFetchWithDoer() expected error")
	}
	if got := err.Error(); !strings.Contains(got, "HTTP 403 from "+kiroRestAPIBase) || !strings.Contains(got, "legacy auth verdict") {
		t.Fatalf("error = %q, want generic operation's last HTTP error", got)
	}
}

func TestRefreshAccountInfo_MapsBonusesFromUsageBreakdown(t *testing.T) {
	var usageCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Get-Usage-Limits", "/getUsageLimits":
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

func TestRefreshAccountInfo_SelectsCreditsBucketAndPrecisionFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Get-Usage-Limits" && r.URL.Path != "/getUsageLimits" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{
			"usageBreakdownList": [
				{
					"resourceType": "AGENTIC_REQUEST",
					"currentUsage": 3337,
					"usageLimit": 10000
				},
				{
					"resourceType": "CREDIT",
					"currentUsage": 9999,
					"currentUsageWithPrecision": 10000,
					"usageLimit": 9999,
					"usageLimitWithPrecision": 10000,
					"freeTrialInfo": {
						"currentUsage": 7,
						"currentUsageWithPrecision": 7.5,
						"usageLimit": 9,
						"usageLimitWithPrecision": 10,
						"freeTrialStatus": "ACTIVE"
					},
					"bonuses": [{
						"bonusCode": "CREDIT_BONUS",
						"currentUsage": 25,
						"usageLimit": 100
					}]
				}
			],
			"subscriptionInfo": {"subscriptionTitle": "KIRO POWER"}
		}`)
	}))
	defer srv.Close()
	installTestRestClient(t, srv)

	info, err := RefreshAccountInfo(&Account{
		AccessToken: "tok",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:1:profile/test",
	})
	if err != nil {
		t.Fatalf("RefreshAccountInfo() error = %v", err)
	}
	if info.UsageCurrent != 10000 || info.UsageLimit != 10000 || info.UsagePercent != 1 {
		t.Fatalf("usage = %v/%v (%v), want Credits precision values 10000/10000 (1)", info.UsageCurrent, info.UsageLimit, info.UsagePercent)
	}
	if info.TrialUsageCurrent != 7.5 || info.TrialUsageLimit != 10 || info.TrialUsagePercent != 0.75 {
		t.Fatalf("trial = %v/%v (%v), want precision values 7.5/10 (0.75)", info.TrialUsageCurrent, info.TrialUsageLimit, info.TrialUsagePercent)
	}
	if len(info.Bonuses) != 1 || info.Bonuses[0].Code != "CREDIT_BONUS" {
		t.Fatalf("bonuses = %+v, want selected Credits bucket bonus", info.Bonuses)
	}
}

func TestUsageValue_PresentZeroPrecisionWinsFallback(t *testing.T) {
	zero := 0.0
	if got := usageValue(&zero, 99); got != 0 {
		t.Fatalf("usageValue(&0, 99) = %v, want 0", got)
	}
	if got := usageValue(nil, 99); got != 99 {
		t.Fatalf("usageValue(nil, 99) = %v, want 99", got)
	}
}

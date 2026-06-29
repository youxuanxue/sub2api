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
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     http.Header{},
	}, nil
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
	installTestRestClient(t, srv)

	doer := &staleProfileChatDoer{}
	account := &Account{
		AccessToken:  "tok",
		RefreshToken: "rt",
		ProfileArn:   "arn:aws:codewhisperer:us-east-1:123456789012:profile/stale",
	}
	if err := CallKiroAPIWithDoer(doer, account, &KiroPayload{}, nil); err != nil {
		t.Fatalf("CallKiroAPIWithDoer() error = %v", err)
	}
	if doer.calls.Load() < 2 {
		t.Fatalf("chat doer calls = %d, want at least 2 (stale pass then fresh retry)", doer.calls.Load())
	}
	if got := account.ProfileArn; got != "arn:aws:codewhisperer:us-east-1:123456789012:profile/fresh" {
		t.Fatalf("account.ProfileArn = %q, want fresh ARN", got)
	}
}

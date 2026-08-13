package kiro

import (
	"net/http"
	"testing"

	tkkiro "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

type capturedKiroHeaders struct {
	userAgent    string
	amzUserAgent string
	optOut       string
}

type kiroHeaderCaptureDoer struct {
	seen []capturedKiroHeaders
}

func (d *kiroHeaderCaptureDoer) Do(req *http.Request) (*http.Response, error) {
	d.seen = append(d.seen, capturedKiroHeaders{
		userAgent:    req.Header.Get("User-Agent"),
		amzUserAgent: req.Header.Get("x-amz-user-agent"),
		optOut:       req.Header.Get("x-amzn-codewhisperer-optout"),
	})
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       http.NoBody,
		Header:     http.Header{},
	}, nil
}

func TestHeaderValuesUseCanonicalKiroCLIIdentity(t *testing.T) {
	got := buildKiroHeaderValues("runtime.us-east-1.kiro.dev")
	if got.UserAgent != tkkiro.KiroCLIUserAgent {
		t.Fatalf("User-Agent drifted:\n got: %s\nwant: %s", got.UserAgent, tkkiro.KiroCLIUserAgent)
	}
	if got.AmzUserAgent != tkkiro.KiroCLIAmzUserAgent {
		t.Fatalf("x-amz-user-agent drifted:\n got: %s\nwant: %s", got.AmzUserAgent, tkkiro.KiroCLIAmzUserAgent)
	}
}

func TestRequestPathsUseCanonicalKiroCLIHeaders(t *testing.T) {
	tests := []struct {
		name string
		run  func(*kiroHeaderCaptureDoer, *Account) error
	}{
		{
			name: "streaming",
			run: func(doer *kiroHeaderCaptureDoer, account *Account) error {
				return CallKiroAPIWithDoer(doer, account, &KiroPayload{}, nil)
			},
		},
		{
			name: "runtime",
			run: func(doer *kiroHeaderCaptureDoer, account *Account) error {
				_, err := getUsageLimitsWithDoer(account, doer)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				AccessToken: "token",
				ProfileArn:  "arn:aws:codewhisperer:us-east-1:1:profile/test",
				MachineId:   "machine-1",
			}
			doer := &kiroHeaderCaptureDoer{}
			if err := tt.run(doer, account); err == nil {
				t.Fatal("test doer must terminate the request with HTTP 401")
			}
			if len(doer.seen) == 0 {
				t.Fatal("actual request path did not reach the injected doer")
			}
			for i, got := range doer.seen {
				if got.userAgent != tkkiro.KiroCLIUserAgent {
					t.Fatalf("request %d User-Agent drifted:\n got: %s\nwant: %s", i, got.userAgent, tkkiro.KiroCLIUserAgent)
				}
				if got.amzUserAgent != tkkiro.KiroCLIAmzUserAgent {
					t.Fatalf("request %d x-amz-user-agent drifted:\n got: %s\nwant: %s", i, got.amzUserAgent, tkkiro.KiroCLIAmzUserAgent)
				}
				if got.optOut != "false" {
					t.Fatalf("request %d opt-out drifted: got %q, want false", i, got.optOut)
				}
			}
		})
	}
}

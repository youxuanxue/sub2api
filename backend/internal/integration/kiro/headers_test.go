package kiro

import (
	"net/http"
	"testing"

	tkkiro "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

type capturedKiroHeaders struct {
	userAgent    string
	amzUserAgent string
}

type kiroHeaderCaptureDoer struct {
	seen []capturedKiroHeaders
}

func (d *kiroHeaderCaptureDoer) Do(req *http.Request) (*http.Response, error) {
	d.seen = append(d.seen, capturedKiroHeaders{
		userAgent:    req.Header.Get("User-Agent"),
		amzUserAgent: req.Header.Get("x-amz-user-agent"),
	})
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       http.NoBody,
		Header:     http.Header{},
	}, nil
}

func TestHeaderValuesUseCanonicalKiroIdentity(t *testing.T) {
	tests := []struct {
		name       string
		override   string
		apiName    string
		sdkVersion string
		mode       string
		build      func(*Account, string) kiroHeaderValues
	}{
		{
			name:       "streaming default",
			apiName:    "codewhispererstreaming",
			sdkVersion: tkkiro.StreamingSDKVersion,
			mode:       "m/E",
			build:      buildStreamingHeaderValues,
		},
		{
			name:       "runtime env override",
			override:   "9.9.9-test",
			apiName:    "codewhispererruntime",
			sdkVersion: tkkiro.RuntimeSDKVersion,
			mode:       "m/N,E",
			build:      buildRuntimeHeaderValues,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tkkiro.UserAgentVersionEnv, tt.override)
			account := &Account{MachineId: "machine-1"}
			got := tt.build(account, "runtime.us-east-1.kiro.dev")
			identity := tkkiro.ResolveClientIdentity()

			wantUA := tkkiro.BuildUserAgent(identity, tt.apiName, tt.sdkVersion, tt.mode, account.MachineId)
			if got.UserAgent != wantUA {
				t.Fatalf("User-Agent drifted from canonical builder:\n got: %s\nwant: %s", got.UserAgent, wantUA)
			}
			wantAmzUA := tkkiro.BuildAmzUserAgent(identity, tt.sdkVersion, account.MachineId)
			if got.AmzUserAgent != wantAmzUA {
				t.Fatalf("x-amz-user-agent drifted from canonical builder:\n got: %s\nwant: %s", got.AmzUserAgent, wantAmzUA)
			}
		})
	}
}

func TestRequestPathsUseCanonicalKiroHeaders(t *testing.T) {
	tests := []struct {
		name       string
		override   string
		apiName    string
		sdkVersion string
		mode       string
		run        func(*kiroHeaderCaptureDoer, *Account) error
	}{
		{
			name:       "streaming",
			apiName:    "codewhispererstreaming",
			sdkVersion: tkkiro.StreamingSDKVersion,
			mode:       "m/E",
			run: func(doer *kiroHeaderCaptureDoer, account *Account) error {
				return CallKiroAPIWithDoer(doer, account, &KiroPayload{}, nil)
			},
		},
		{
			name:       "runtime",
			override:   "9.9.9-test",
			apiName:    "codewhispererruntime",
			sdkVersion: tkkiro.RuntimeSDKVersion,
			mode:       "m/N,E",
			run: func(doer *kiroHeaderCaptureDoer, account *Account) error {
				_, err := getUsageLimitsWithDoer(account, doer)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tkkiro.UserAgentVersionEnv, tt.override)
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

			identity := tkkiro.ResolveClientIdentity()
			wantUA := tkkiro.BuildUserAgent(identity, tt.apiName, tt.sdkVersion, tt.mode, account.MachineId)
			wantAmzUA := tkkiro.BuildAmzUserAgent(identity, tt.sdkVersion, account.MachineId)
			for i, got := range doer.seen {
				if got.userAgent != wantUA {
					t.Fatalf("request %d User-Agent drifted:\n got: %s\nwant: %s", i, got.userAgent, wantUA)
				}
				if got.amzUserAgent != wantAmzUA {
					t.Fatalf("request %d x-amz-user-agent drifted:\n got: %s\nwant: %s", i, got.amzUserAgent, wantAmzUA)
				}
			}
		})
	}
}

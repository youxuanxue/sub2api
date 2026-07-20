package kiro

import (
	"testing"

	tkkiro "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

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

//go:build unit

package kiro

import "testing"

func TestKiroCLIIdentityMatchesObservedCapture(t *testing.T) {
	const wantUA = "aws-sdk-rust/1.3.10 ua/2.1 api/codewhispererruntime/0.1.10231 os/macos lang/rust/1.92.0 md/appVersion-2.21.1 app/AmazonQ-For-CLI"
	const wantAmzUA = "aws-sdk-rust/1.3.10 ua/2.1 api/codewhispererruntime/0.1.10231 os/macos lang/rust/1.92.0 m/F app/AmazonQ-For-CLI"
	if KiroCLIUserAgent != wantUA {
		t.Fatalf("unexpected Kiro CLI User-Agent:\n got: %s\nwant: %s", KiroCLIUserAgent, wantUA)
	}
	if KiroCLIAmzUserAgent != wantAmzUA {
		t.Fatalf("unexpected Kiro CLI x-amz-user-agent:\n got: %s\nwant: %s", KiroCLIAmzUserAgent, wantAmzUA)
	}
}

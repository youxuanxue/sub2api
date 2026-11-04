//go:build unit

package kiro

import "testing"

func TestKiroCLIIdentityMatchesObservedCapture(t *testing.T) {
	const wantUA = "aws-sdk-rust/1.6.0 ua/2.1 api/codewhispererstreaming/0.1.19636 os/macos lang/rust/1.94.1 md/appVersion-3.0.0 app/AmazonQ-For-CLI"
	const wantAmzUA = "aws-sdk-rust/1.6.0 ua/2.1 api/codewhispererstreaming/0.1.19636 os/macos lang/rust/1.94.1 m/F app/AmazonQ-For-CLI"
	if KiroCLIUserAgent != wantUA {
		t.Fatalf("unexpected Kiro CLI User-Agent:\n got: %s\nwant: %s", KiroCLIUserAgent, wantUA)
	}
	if KiroCLIAmzUserAgent != wantAmzUA {
		t.Fatalf("unexpected Kiro CLI x-amz-user-agent:\n got: %s\nwant: %s", KiroCLIAmzUserAgent, wantAmzUA)
	}
}

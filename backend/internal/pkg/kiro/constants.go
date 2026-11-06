// Package kiro owns TokenKey-controlled Kiro runtime constants.
package kiro

// DefaultKiroAccountPriority is the creation-time scheduler priority default for
// native Kiro accounts when the caller omits priority (0). Smaller priority wins
// in the scheduler; after creation, admin edits own the account priority.
const DefaultKiroAccountPriority = 10

// DefaultKiroCLIVersion is the sole Kiro client release owner. The HTTP identity
// below is the User-Agent observed on
// AmazonCodeWhispererStreamingService.GenerateAssistantResponse from logged-in
// kiro-cli 3.1.0; do not infer any segment from release metadata alone.
const DefaultKiroCLIVersion = "3.1.0"

const (
	kiroCLISDKVersion    = "1.6.0"
	kiroCLIAPIVersion    = "0.1.19636"
	kiroCLIRustVersion   = "1.94.1"
	kiroCLIUserAgentBase = "aws-sdk-rust/" + kiroCLISDKVersion + " ua/2.1 api/codewhispererstreaming/" + kiroCLIAPIVersion + " os/macos lang/rust/" + kiroCLIRustVersion
	KiroCLIUserAgent     = kiroCLIUserAgentBase + " md/appVersion-" + DefaultKiroCLIVersion + " app/AmazonQ-For-CLI"
	KiroCLIAmzUserAgent  = kiroCLIUserAgentBase + " m/F app/AmazonQ-For-CLI"
)

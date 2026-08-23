// Package kiro owns TokenKey-controlled Kiro runtime constants.
package kiro

// DefaultKiroAccountPriority is the creation-time scheduler priority default for
// native Kiro accounts when the caller omits priority (0). Smaller priority wins
// in the scheduler; after creation, admin edits own the account priority.
const DefaultKiroAccountPriority = 10

// DefaultKiroCLIVersion is the sole Kiro client release owner. The HTTP identity
// below is captured from the installed kiro-cli binary; do not infer any segment
// from release metadata alone.
const DefaultKiroCLIVersion = "2.19.1"

const (
	kiroCLISDKVersion    = "1.3.10"
	kiroCLIAPIVersion    = "0.1.10231"
	kiroCLIRustVersion   = "1.92.0"
	kiroCLIUserAgentBase = "aws-sdk-rust/" + kiroCLISDKVersion + " ua/2.1 api/codewhispererruntime/" + kiroCLIAPIVersion + " os/macos lang/rust/" + kiroCLIRustVersion
	KiroCLIUserAgent     = kiroCLIUserAgentBase + " md/appVersion-" + DefaultKiroCLIVersion + " app/AmazonQ-For-CLI"
	KiroCLIAmzUserAgent  = kiroCLIUserAgentBase + " m/F app/AmazonQ-For-CLI"
)

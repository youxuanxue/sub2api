package antigravity

import (
	"regexp"
)

// Sentences the Antigravity upstream rejects with a fake RESOURCE_EXHAUSTED
// 429 (no quotaResetDelay / RetryInfo). Keep patterns specific: only strip
// known trigger fragments, never broad security-policy text.
//
// Contract evidence: Wei-Shaw/sub2api#8824 (Claude Code Dual-use sentence).
var defaultUpstreamTriggerPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)Dual-use security tools \([^)]*\) require clear authorization context:[^.\n]*\.[ \t]*`),
}

// StripUpstreamTriggerSentences removes known Antigravity content-gate
// fragments from system text. Only Antigravity forwarding should call this.
func StripUpstreamTriggerSentences(text string) string {
	for _, re := range defaultUpstreamTriggerPatterns {
		text = re.ReplaceAllString(text, "")
	}
	return text
}

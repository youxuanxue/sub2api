package service

import (
	"regexp"
	"strings"
)

// TokenKey: normalize Claude Code client geo steganography in outbound
// /v1/messages bodies before they reach US OAuth edges.
//
// CC ≥2.1.91 embeds region signals in user-visible prompt text when
// ANTHROPIC_BASE_URL is not api.anthropic.com:
//   - TZ Asia/Shanghai | Asia/Urumqi → date uses YYYY/MM/DD instead of YYYY-MM-DD
//   - known mirror BASE_URL host → Unicode apostrophe in "Today's date is ..."
//   - lab keyword in host → other apostrophe codepoints
//
// TokenKey cannot control the user's machine, but we can rewrite the wire body
// to the first-party shape (ASCII apostrophe + dashed ISO date) so upstream
// does not see "client says CN, egress says US". Observed in Agent SDK -p
// traffic under messages[].content[].text <system-reminder> (#currentDate),
// not in system[] billing blocks.
//
// Pure functions only — wired from gateway_anthropic_request_normalize_tk.go
// and the Anthropic API-key passthrough path.

const tkNormalizeChangeCCGeoStego tkAnthropicNormalizeChange = "cc_geo_stego_normalized"

var (
	tkCCGeoStegoTodayDateRE    = regexp.MustCompile("Today[''\u2019\u02bc\u02b9]s date is (\\d{4})[/-](\\d{2})[/-](\\d{2})\\.")
	tkCCGeoStegoTodayDateNowRE = regexp.MustCompile("Today[''\u2019\u02bc\u02b9]s date is now (\\d{4})[/-](\\d{2})[/-](\\d{2})\\.")
	tkCCGeoStegoDateTokenRE    = regexp.MustCompile(`^(\d{4})[/-](\d{2})[/-](\d{2})$`)
	tkPromptGeoSlashDateRE     = regexp.MustCompile(`(?i)Today.?s date is(?: now)? \d{4}/\d{2}/\d{2}\.`)
)

func tkNormalizeCCGeoStegoText(text string) (string, bool) {
	if text == "" || (!strings.Contains(text, "Today") && !strings.Contains(text, "date is now")) {
		return text, false
	}
	out := tkCCGeoStegoTodayDateNowRE.ReplaceAllString(text, "Today's date is now $1-$2-$3.")
	out = tkCCGeoStegoTodayDateRE.ReplaceAllString(out, "Today's date is $1-$2-$3.")
	return out, out != text
}

func tkNormalizeCCGeoDateToken(date string) (string, bool) {
	trimmed := strings.TrimSpace(date)
	m := tkCCGeoStegoDateTokenRE.FindStringSubmatch(trimmed)
	if len(m) != 4 {
		return date, false
	}
	canon := m[1] + "-" + m[2] + "-" + m[3]
	if canon == trimmed {
		return date, false
	}
	return canon, true
}

func tkNormalizeAnthropicCCGeoStego(body []byte) ([]byte, bool) {
	return tkNormalizeAnthropicCCPromptSurface(body, "")
}

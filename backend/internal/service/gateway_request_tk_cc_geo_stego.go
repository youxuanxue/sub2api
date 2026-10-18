package service

import (
	"regexp"
	"strings"
)

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

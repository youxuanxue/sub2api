package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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
	if len(body) == 0 {
		return body, false
	}

	out := body
	changed := false

	if patched, applied := tkNormalizeAnthropicCCGeoStegoSystem(out); applied {
		out = patched
		changed = true
	}
	if patched, applied := tkNormalizeAnthropicCCGeoStegoMessages(out); applied {
		out = patched
		changed = true
	}
	return out, changed
}

func tkNormalizeAnthropicCCGeoStegoSystem(body []byte) ([]byte, bool) {
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		return body, false
	}

	switch system.Type {
	case gjson.String:
		newText, ok := tkNormalizeCCGeoStegoText(system.String())
		if !ok {
			return body, false
		}
		out, err := sjson.SetBytes(body, "system", newText)
		if err != nil {
			return body, false
		}
		return out, true
	case gjson.JSON:
		if !system.IsArray() {
			return body, false
		}
		out := body
		changed := false
		for i, item := range system.Array() {
			if item.Get("type").String() != "text" {
				continue
			}
			text := item.Get("text").String()
			newText, ok := tkNormalizeCCGeoStegoText(text)
			if !ok {
				continue
			}
			path := fmt.Sprintf("system.%d.text", i)
			next, err := sjson.SetBytes(out, path, newText)
			if err != nil {
				continue
			}
			out = next
			changed = true
		}
		return out, changed
	default:
		return body, false
	}
}

func tkNormalizeAnthropicCCGeoStegoMessages(body []byte) ([]byte, bool) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body, false
	}

	out := body
	changed := false
	for mi, msg := range messages.Array() {
		content := msg.Get("content")
		switch content.Type {
		case gjson.String:
			newText, ok := tkNormalizeCCGeoStegoText(content.String())
			if !ok {
				continue
			}
			path := fmt.Sprintf("messages.%d.content", mi)
			next, err := sjson.SetBytes(out, path, newText)
			if err != nil {
				continue
			}
			out = next
			changed = true
		case gjson.JSON:
			if !content.IsArray() {
				continue
			}
			for ci, block := range content.Array() {
				blockType := block.Get("type").String()
				if blockType == "text" {
					text := block.Get("text").String()
					newText, ok := tkNormalizeCCGeoStegoText(text)
					if ok {
						path := fmt.Sprintf("messages.%d.content.%d.text", mi, ci)
						next, err := sjson.SetBytes(out, path, newText)
						if err == nil {
							out = next
							changed = true
						}
					}
				}
				if block.Get("attachment.type").String() == "date_change" {
					date := block.Get("attachment.newDate").String()
					newDate, ok := tkNormalizeCCGeoDateToken(date)
					if ok {
						path := fmt.Sprintf("messages.%d.content.%d.attachment.newDate", mi, ci)
						next, err := sjson.SetBytes(out, path, newDate)
						if err == nil {
							out = next
							changed = true
						}
					}
				}
			}
		}
	}
	return out, changed
}

var tkWireCCGeoStegoSlashDateRE = regexp.MustCompile(`Today's date is \d{4}/\d{2}/\d{2}\.`)
var tkPromptGeoSlashDateRE = regexp.MustCompile(`(?i)Today.?s date is(?: now)? \d{4}/\d{2}/\d{2}\.`)

func tkWireStillHasCCGeoStegoDateSignals(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	s := string(body)
	if tkPromptGeoSlashDateRE.MatchString(s) || tkWireCCGeoStegoSlashDateRE.MatchString(s) {
		return true
	}
	if strings.Contains(s, "Today\u2019s") ||
		strings.Contains(s, "Today\u02bcs") ||
		strings.Contains(s, "Today\u02b9s") {
		return true
	}
	return false
}

package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	tkNormalizeChangeCCEnvironmentStrip  tkAnthropicNormalizeChange = "cc_environment_stripped"
	tkNormalizeChangeCCUserEmailReplaced tkAnthropicNormalizeChange = "cc_user_email_replaced"
)

var (
	tkCCUserEmailLineRE  = regexp.MustCompile(`(?m)^The user's email address is [^\n]+\.\s*$`)
	tkCCWireCNTimezoneRE = regexp.MustCompile(`TZ=Asia/(Shanghai|Urumqi)`)
)

func tkNormalizeCCPromptSurfaceText(text, oauthEmail string) (string, bool) {
	if text == "" {
		return text, false
	}
	changed := false
	if out, ok := tkStripCCEnvironmentSection(text); ok {
		text = out
		changed = true
	}
	if out, ok := tkNormalizeCCUserEmailLine(text, oauthEmail); ok {
		text = out
		changed = true
	}
	if out, ok := tkNormalizeCCGeoStegoText(text); ok {
		text = out
		changed = true
	}
	return text, changed
}

func tkIsCCSystemReminderText(text string) bool {
	return strings.Contains(strings.ToLower(text), "<system-reminder>")
}

func tkIsCCSystemPromptText(text string) bool {
	if strings.Contains(text, claudeCodeBillingHeaderPrefix) {
		return true
	}
	id := tkMatchPromptIdentityAnchor(text)
	return id != tkIdentityAnchorAbsent && id != tkIdentityAnchorUnknown
}

func tkStripCCEnvironmentSection(text string) (string, bool) {
	if !strings.Contains(text, "# Environment") {
		return text, false
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	skip := false
	changed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "# Environment" {
			skip = true
			changed = true
			continue
		}
		if skip {
			if strings.HasPrefix(trimmed, "# ") {
				skip = false
				out = append(out, line)
				continue
			}
			if tkIsCCEnvironmentKVLine(trimmed) {
				continue
			}
			skip = false
		}
		out = append(out, line)
	}
	if !changed {
		return text, false
	}
	return strings.TrimSpace(strings.Join(out, "\n")), true
}

func tkIsCCEnvironmentKVLine(line string) bool {
	if line == "" {
		return true
	}
	for _, prefix := range []string{"TZ=", "Proxy=", "proxy=", "PWD=", "cwd="} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func tkNormalizeCCUserEmailLine(text, oauthEmail string) (string, bool) {
	if !strings.Contains(text, "The user's email address is") {
		return text, false
	}
	oauthEmail = strings.TrimSpace(oauthEmail)
	var out string
	if oauthEmail == "" {
		out = tkCCUserEmailLineRE.ReplaceAllString(text, "")
	} else {
		replacement := "The user's email address is " + oauthEmail + "."
		out = tkCCUserEmailLineRE.ReplaceAllString(text, replacement)
	}
	out = tkCollapsePromptBlankLines(out)
	if out == text {
		return text, false
	}
	return out, true
}

func tkCollapsePromptBlankLines(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if blank {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func tkNormalizeAnthropicCCPromptSurface(body []byte, oauthEmail string) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	out := body
	changed := false
	if patched, applied := tkNormalizeAnthropicCCPromptSurfaceSystem(out, oauthEmail); applied {
		out = patched
		changed = true
	}
	if patched, applied := tkNormalizeAnthropicCCPromptSurfaceMessages(out, oauthEmail); applied {
		out = patched
		changed = true
	}
	return out, changed
}

func tkNormalizeAnthropicCCPromptSurfaceSystem(body []byte, oauthEmail string) ([]byte, bool) {
	system := gjson.GetBytes(body, "system")
	if !system.Exists() {
		return body, false
	}
	switch system.Type {
	case gjson.String:
		if !tkIsCCSystemPromptText(system.String()) {
			return body, false
		}
		newText, ok := tkNormalizeCCPromptSurfaceText(system.String(), oauthEmail)
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
			if !tkIsCCSystemPromptText(text) {
				continue
			}
			newText, ok := tkNormalizeCCPromptSurfaceText(text, oauthEmail)
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

func tkNormalizeAnthropicCCPromptSurfaceMessages(body []byte, oauthEmail string) ([]byte, bool) {
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
			if !tkIsCCSystemReminderText(content.String()) {
				continue
			}
			newText, ok := tkNormalizeCCPromptSurfaceText(content.String(), oauthEmail)
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
				if block.Get("type").String() == "text" {
					text := block.Get("text").String()
					if tkIsCCSystemReminderText(text) {
						newText, ok := tkNormalizeCCPromptSurfaceText(text, oauthEmail)
						if ok {
							path := fmt.Sprintf("messages.%d.content.%d.text", mi, ci)
							next, err := sjson.SetBytes(out, path, newText)
							if err == nil {
								out = next
								changed = true
							}
						}
					}
				}
				if block.Get("attachment.type").String() == "date_change" {
					newDate, ok := tkNormalizeCCGeoDateToken(block.Get("attachment.newDate").String())
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

func tkWireStillHasCCPromptSurfaceLeaks(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if tkWireStillHasCCGeoStegoDateSignals(body) {
		return true
	}
	s := string(body)
	if strings.Contains(s, "# Environment") {
		return true
	}
	return tkCCWireCNTimezoneRE.MatchString(s)
}

func tkAnthropicCCPromptSurfaceChanges(before, after []byte) []tkAnthropicNormalizeChange {
	if len(before) == 0 || len(after) == 0 || string(before) == string(after) {
		return nil
	}
	beforeS, afterS := string(before), string(after)
	var changes []tkAnthropicNormalizeChange
	if tkWireStillHasCCGeoStegoDateSignals(before) && !tkWireStillHasCCGeoStegoDateSignals(after) {
		changes = append(changes, tkNormalizeChangeCCGeoStego)
	}
	if strings.Contains(beforeS, "# Environment") && !strings.Contains(afterS, "# Environment") {
		changes = append(changes, tkNormalizeChangeCCEnvironmentStrip)
	}
	beforeEmail := tkCCUserEmailLineRE.FindString(beforeS)
	afterEmail := tkCCUserEmailLineRE.FindString(afterS)
	if beforeEmail != "" && beforeEmail != afterEmail {
		changes = append(changes, tkNormalizeChangeCCUserEmailReplaced)
	}
	return changes
}

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

type tkCCPromptSurfaceClass string

const (
	tkCCPromptSurfaceGenericUserText tkCCPromptSurfaceClass = "generic_user_text"
	tkCCPromptSurfaceGenericSystem   tkCCPromptSurfaceClass = "generic_system_text"
	tkCCPromptSurfaceKnownSystem     tkCCPromptSurfaceClass = "known_cc_system"
	tkCCPromptSurfaceSystemReminder  tkCCPromptSurfaceClass = "cc_system_reminder"
	tkCCPromptSurfaceDateChange      tkCCPromptSurfaceClass = "cc_date_change_attachment"
	tkCCPromptSurfaceUnknownSystem   tkCCPromptSurfaceClass = "unknown_system"
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

func tkClassifyCCPromptSurfaceText(text string, inSystem bool) tkCCPromptSurfaceClass {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return tkCCPromptSurfaceGenericUserText
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "<system-reminder>") {
		return tkCCPromptSurfaceSystemReminder
	}
	if !inSystem {
		return tkCCPromptSurfaceGenericUserText
	}
	if strings.Contains(text, claudeCodeBillingHeaderPrefix) {
		return tkCCPromptSurfaceKnownSystem
	}
	id := tkMatchPromptIdentityAnchor(text)
	if id != tkIdentityAnchorAbsent && id != tkIdentityAnchorUnknown {
		return tkCCPromptSurfaceKnownSystem
	}
	if tkCCPromptSurfaceTextHasPromptSignal(text) && tkLooksLikePromptIdentityText(text) {
		return tkCCPromptSurfaceUnknownSystem
	}
	return tkCCPromptSurfaceGenericSystem
}

func tkCCPromptSurfaceClassNormalizes(cls tkCCPromptSurfaceClass) bool {
	return cls == tkCCPromptSurfaceKnownSystem || cls == tkCCPromptSurfaceSystemReminder
}

func tkCCPromptSurfaceClassTracksLeaks(cls tkCCPromptSurfaceClass) bool {
	switch cls {
	case tkCCPromptSurfaceKnownSystem, tkCCPromptSurfaceSystemReminder, tkCCPromptSurfaceUnknownSystem:
		return true
	default:
		return false
	}
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
		if !tkCCPromptSurfaceClassNormalizes(tkClassifyCCPromptSurfaceText(system.String(), true)) {
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
			if !tkCCPromptSurfaceClassNormalizes(tkClassifyCCPromptSurfaceText(text, true)) {
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
			if !tkCCPromptSurfaceClassNormalizes(tkClassifyCCPromptSurfaceText(content.String(), false)) {
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
					if tkCCPromptSurfaceClassNormalizes(tkClassifyCCPromptSurfaceText(text, false)) {
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
	return len(tkCCPromptSurfaceBodyUnknownSurfaces(body)) > 0
}

func tkCCPromptSurfaceBodyUnknownSurfaces(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	var surfaces []string
	collectText := func(text string, inSystem bool) {
		cls := tkClassifyCCPromptSurfaceText(text, inSystem)
		if !tkCCPromptSurfaceClassTracksLeaks(cls) {
			return
		}
		for _, surface := range tkCCPromptSurfaceTextUnknownSurfaces(text) {
			surfaces = appendUniqueString(surfaces, surface)
		}
	}

	system := gjson.GetBytes(body, "system")
	switch system.Type {
	case gjson.String:
		collectText(system.String(), true)
	case gjson.JSON:
		if system.IsArray() {
			for _, item := range system.Array() {
				if item.Get("type").String() == "text" {
					collectText(item.Get("text").String(), true)
				}
			}
		}
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return surfaces
	}
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		switch content.Type {
		case gjson.String:
			collectText(content.String(), false)
		case gjson.JSON:
			if !content.IsArray() {
				continue
			}
			for _, block := range content.Array() {
				if block.Get("type").String() == "text" {
					collectText(block.Get("text").String(), false)
				}
				if block.Get("attachment.type").String() == "date_change" {
					for _, surface := range tkCCPromptSurfaceDateChangeUnknownSurfaces(block.Get("attachment.newDate").String()) {
						surfaces = appendUniqueString(surfaces, surface)
					}
				}
			}
		}
	}
	return surfaces
}

func tkCCPromptSurfaceTextUnknownSurfaces(text string) []string {
	var surfaces []string
	dateClass := tkClassifyGeoStegoDateLine(text)
	if dateClass != "NONE" && dateClass != "ISO_DASH_ASCII" {
		surfaces = appendUniqueString(surfaces, "geo_stego_date_line")
	}
	if strings.Contains(text, "# Environment") || tkCCWireCNTimezoneRE.MatchString(text) {
		surfaces = appendUniqueString(surfaces, "cc_environment_section")
	}
	return surfaces
}

func tkCCPromptSurfaceTextHasPromptSignal(text string) bool {
	if len(tkCCPromptSurfaceTextUnknownSurfaces(text)) > 0 {
		return true
	}
	return strings.Contains(text, "The user's email address is")
}

func tkLooksLikePromptIdentityText(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return strings.HasPrefix(trimmed, "You are ")
	}
	return false
}

func tkCCPromptSurfaceDateChangeUnknownSurfaces(date string) []string {
	switch tkClassifyGeoStegoDateToken(date) {
	case "NONE", "ISO_DASH_ASCII":
		return nil
	default:
		return []string{"geo_stego_date_line"}
	}
}

func tkCCPromptSurfaceBodyUserEmailLine(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	found := ""
	collectText := func(text string, inSystem bool) {
		if found != "" {
			return
		}
		cls := tkClassifyCCPromptSurfaceText(text, inSystem)
		if !tkCCPromptSurfaceClassNormalizes(cls) {
			return
		}
		found = tkCCUserEmailLineRE.FindString(text)
	}

	system := gjson.GetBytes(body, "system")
	switch system.Type {
	case gjson.String:
		collectText(system.String(), true)
	case gjson.JSON:
		if system.IsArray() {
			for _, item := range system.Array() {
				if item.Get("type").String() == "text" {
					collectText(item.Get("text").String(), true)
				}
			}
		}
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return found
	}
	for _, msg := range messages.Array() {
		content := msg.Get("content")
		switch content.Type {
		case gjson.String:
			collectText(content.String(), false)
		case gjson.JSON:
			if !content.IsArray() {
				continue
			}
			for _, block := range content.Array() {
				if block.Get("type").String() == "text" {
					collectText(block.Get("text").String(), false)
				}
			}
		}
	}
	return found
}

func tkAnthropicCCPromptSurfaceChanges(before, after []byte) []tkAnthropicNormalizeChange {
	if len(before) == 0 || len(after) == 0 || string(before) == string(after) {
		return nil
	}
	var changes []tkAnthropicNormalizeChange
	beforeUnknown := tkCCPromptSurfaceBodyUnknownSurfaces(before)
	afterUnknown := tkCCPromptSurfaceBodyUnknownSurfaces(after)
	if stringSliceContains(beforeUnknown, "geo_stego_date_line") && !stringSliceContains(afterUnknown, "geo_stego_date_line") {
		changes = append(changes, tkNormalizeChangeCCGeoStego)
	}
	if stringSliceContains(beforeUnknown, "cc_environment_section") && !stringSliceContains(afterUnknown, "cc_environment_section") {
		changes = append(changes, tkNormalizeChangeCCEnvironmentStrip)
	}
	beforeEmail := tkCCPromptSurfaceBodyUserEmailLine(before)
	afterEmail := tkCCPromptSurfaceBodyUserEmailLine(after)
	if beforeEmail != "" && beforeEmail != afterEmail {
		changes = append(changes, tkNormalizeChangeCCUserEmailReplaced)
	}
	return changes
}

func stringSliceContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

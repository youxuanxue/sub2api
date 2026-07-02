package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// TokenKey: structured outbound /v1/messages prompt fingerprint (hashes + classes,
// never full system text). Used for prod aggregation and drift detection against
// ops/anthropic/prompt_surface_registry.json.

const tkPromptFingerprintLogKey = "gateway.anthropic_prompt_fingerprint"

const (
	tkIdentityAnchorAbsent  = "absent"
	tkIdentityAnchorUnknown = "unknown"
)

var tkPromptSurfaceIdentityPrefixes = []struct {
	id     string
	prefix string
}{
	{"claude_code_cli", "You are Claude Code, Anthropic's official CLI for Claude"},
	{"claude_agent_sdk", "You are a Claude agent, built on Anthropic's Claude Agent SDK"},
	{"interactive_agent", "You are an interactive agent that helps users with software engineering tasks"},
	{"interactive_cli_tool", "You are an interactive CLI tool that helps users"},
	{"file_search_specialist", "You are a file search specialist for Claude Code"},
	{"summarization_assistant", "You are a helpful AI assistant tasked with summarizing conversations"},
}

type tkAnthropicPromptFingerprint struct {
	SystemBlockCount      int
	IdentityAnchorID      string
	BillingPrefixPresent  bool
	HasSystemReminder     bool
	PromptSurfaceClasses  []tkCCPromptSurfaceClass
	ReminderDateLineClass string
	GeoStegoCanonical     bool
	SurfaceSignature      string
	UnknownSurfaces       []string
}

func tkExtractAnthropicPromptFingerprint(body []byte) tkAnthropicPromptFingerprint {
	fp := tkAnthropicPromptFingerprint{
		IdentityAnchorID:      tkIdentityAnchorAbsent,
		ReminderDateLineClass: "NONE",
		GeoStegoCanonical:     true,
	}
	if len(body) == 0 {
		fp.SurfaceSignature = tkPromptSurfaceSignature(fp)
		return fp
	}

	system := gjson.GetBytes(body, "system")
	switch system.Type {
	case gjson.String:
		fp.SystemBlockCount = 1
		tkApplySystemTextFingerprint(system.String(), &fp)
	case gjson.JSON:
		if system.IsArray() {
			fp.SystemBlockCount = len(system.Array())
			for _, item := range system.Array() {
				if item.Get("type").String() != "text" {
					continue
				}
				tkApplySystemTextFingerprint(item.Get("text").String(), &fp)
			}
		}
	}

	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		for _, msg := range messages.Array() {
			tkApplyMessageContentFingerprint(msg.Get("content"), &fp)
		}
	}

	fp.GeoStegoCanonical = !tkWireStillHasCCPromptSurfaceLeaks(body)
	for _, surface := range tkCCPromptSurfaceBodyUnknownSurfaces(body) {
		fp.UnknownSurfaces = appendUniqueString(fp.UnknownSurfaces, surface)
	}
	if fp.ReminderDateLineClass == "NONE" && stringSliceContains(fp.UnknownSurfaces, "geo_stego_date_line") {
		fp.ReminderDateLineClass = "NONCANONICAL"
	}
	fp.SurfaceSignature = tkPromptSurfaceSignature(fp)
	return fp
}

func tkApplySystemTextFingerprint(text string, fp *tkAnthropicPromptFingerprint) {
	if text == "" {
		return
	}
	cls := tkClassifyCCPromptSurfaceText(text, true)
	fp.PromptSurfaceClasses = appendUniquePromptSurfaceClass(fp.PromptSurfaceClasses, cls)
	if strings.HasPrefix(strings.TrimSpace(text), claudeCodeBillingHeaderPrefix) ||
		strings.Contains(text, claudeCodeBillingHeaderPrefix) {
		fp.BillingPrefixPresent = true
	}
	if fp.IdentityAnchorID == tkIdentityAnchorAbsent {
		fp.IdentityAnchorID = tkMatchPromptIdentityAnchor(text)
	}
	if tkCCPromptSurfaceClassTracksLeaks(cls) {
		dateClass := tkClassifyGeoStegoDateLine(text)
		if dateClass != "NONE" {
			fp.ReminderDateLineClass = dateClass
		}
	}
}

func tkMatchPromptIdentityAnchor(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		for _, entry := range tkPromptSurfaceIdentityPrefixes {
			if strings.HasPrefix(trimmed, entry.prefix) {
				return entry.id
			}
		}
	}
	trimmed := strings.TrimSpace(text)
	if trimmed != "" {
		return tkIdentityAnchorUnknown
	}
	return tkIdentityAnchorAbsent
}

func tkApplyMessageContentFingerprint(content gjson.Result, fp *tkAnthropicPromptFingerprint) {
	switch content.Type {
	case gjson.String:
		tkApplyMessageTextFingerprint(content.String(), fp)
	case gjson.JSON:
		if !content.IsArray() {
			return
		}
		for _, block := range content.Array() {
			if block.Get("type").String() == "text" {
				tkApplyMessageTextFingerprint(block.Get("text").String(), fp)
			}
			if block.Get("attachment.type").String() == "date_change" {
				fp.PromptSurfaceClasses = appendUniquePromptSurfaceClass(fp.PromptSurfaceClasses, tkCCPromptSurfaceDateChange)
				date := block.Get("attachment.newDate").String()
				cls := tkClassifyGeoStegoDateToken(date)
				if cls != "NONE" {
					fp.ReminderDateLineClass = cls
					if cls != "ISO_DASH_ASCII" {
						fp.UnknownSurfaces = appendUniqueString(fp.UnknownSurfaces, "geo_stego_date_line")
					}
				}
			}
		}
	}
}

func tkApplyMessageTextFingerprint(text string, fp *tkAnthropicPromptFingerprint) {
	if text == "" {
		return
	}
	surfaceClass := tkClassifyCCPromptSurfaceText(text, false)
	fp.PromptSurfaceClasses = appendUniquePromptSurfaceClass(fp.PromptSurfaceClasses, surfaceClass)
	if surfaceClass == tkCCPromptSurfaceSystemReminder {
		fp.HasSystemReminder = true
	}
	if tkCCPromptSurfaceClassTracksLeaks(surfaceClass) {
		dateClass := tkClassifyGeoStegoDateLine(text)
		if dateClass != "NONE" {
			fp.ReminderDateLineClass = dateClass
		}
	}
}

func tkClassifyGeoStegoDateLine(text string) string {
	if !strings.Contains(text, "Today") && !strings.Contains(text, "date is now") {
		return "NONE"
	}
	hasSlashDate := tkPromptGeoSlashDateRE.MatchString(text)
	hasUnicodeApostrophe := strings.Contains(text, "\u2019") ||
		strings.Contains(text, "\u02bc") ||
		strings.Contains(text, "\u02b9")
	if hasSlashDate || hasUnicodeApostrophe {
		if hasSlashDate && hasUnicodeApostrophe {
			return "SLASH_UNICODE"
		}
		if hasSlashDate {
			return "SLASH_ASCII"
		}
		return "UNICODE_APOSTROPHE"
	}
	if strings.Contains(text, "Today's date is") || strings.Contains(text, "Today's date is now") {
		return "ISO_DASH_ASCII"
	}
	return "OTHER"
}

func tkClassifyGeoStegoDateToken(date string) string {
	trimmed := strings.TrimSpace(date)
	if trimmed == "" {
		return "NONE"
	}
	if strings.Contains(trimmed, "/") {
		return "SLASH_ASCII"
	}
	if tkCCGeoStegoDateTokenRE.MatchString(trimmed) {
		return "ISO_DASH_ASCII"
	}
	return "OTHER"
}

func tkPromptSurfaceSignature(fp tkAnthropicPromptFingerprint) string {
	classParts := make([]string, 0, len(fp.PromptSurfaceClasses))
	for _, cls := range fp.PromptSurfaceClasses {
		classParts = append(classParts, string(cls))
	}
	raw := fmt.Sprintf(
		"sys=%d|id=%s|bill=%t|rem=%t|classes=%s|date=%s|geo=%t|unk=%s",
		fp.SystemBlockCount,
		fp.IdentityAnchorID,
		fp.BillingPrefixPresent,
		fp.HasSystemReminder,
		strings.Join(classParts, "+"),
		fp.ReminderDateLineClass,
		fp.GeoStegoCanonical,
		strings.Join(fp.UnknownSurfaces, "+"),
	)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

func appendUniquePromptSurfaceClass(list []tkCCPromptSurfaceClass, value tkCCPromptSurfaceClass) []tkCCPromptSurfaceClass {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

func appendUniqueString(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

func (fp tkAnthropicPromptFingerprint) shouldLogPromptFingerprint(
	normalizeChanges []tkAnthropicNormalizeChange,
	requestID string,
) bool {
	if len(normalizeChanges) > 0 {
		return true
	}
	if len(fp.UnknownSurfaces) > 0 {
		return true
	}
	if fp.IdentityAnchorID == tkIdentityAnchorUnknown && fp.SystemBlockCount > 0 {
		// CC-shaped system traffic with an unrecognized anchor is load-bearing drift;
		// generic custom system prompts fall through to baseline sampling only.
		if fp.BillingPrefixPresent ||
			fp.HasSystemReminder ||
			promptSurfaceClassesContain(fp.PromptSurfaceClasses, tkCCPromptSurfaceUnknownSystem) ||
			len(fp.UnknownSurfaces) > 0 ||
			!fp.GeoStegoCanonical {
			return true
		}
	}
	if !fp.GeoStegoCanonical {
		return true
	}
	if requestID == "" {
		return false
	}
	sum := sha256.Sum256([]byte(requestID))
	return sum[0] < 3 // ~1.2% baseline sample
}

func (s *GatewayService) tkMaybeLogAnthropicPromptFingerprint(
	ctx context.Context,
	c *gin.Context,
	body []byte,
	normalizeChanges []tkAnthropicNormalizeChange,
) {
	if s == nil || len(body) == 0 {
		return
	}
	fp := tkExtractAnthropicPromptFingerprint(body)
	requestID, _ := ctx.Value(ctxkey.RequestID).(string)
	if !fp.shouldLogPromptFingerprint(normalizeChanges, requestID) {
		return
	}
	tkLogAnthropicPromptFingerprint(ctx, fp, normalizeChanges)
	_ = c
}

func tkLogAnthropicPromptFingerprint(
	ctx context.Context,
	fp tkAnthropicPromptFingerprint,
	normalizeChanges []tkAnthropicNormalizeChange,
) {
	parts := make([]string, 0, len(normalizeChanges))
	for _, ch := range normalizeChanges {
		parts = append(parts, string(ch))
	}
	requestID, _ := ctx.Value(ctxkey.RequestID).(string)
	attrs := []any{
		slog.String("request_id", requestID),
		slog.Int("system_block_count", fp.SystemBlockCount),
		slog.String("identity_anchor_id", fp.IdentityAnchorID),
		slog.Bool("billing_prefix_present", fp.BillingPrefixPresent),
		slog.Bool("has_system_reminder", fp.HasSystemReminder),
		slog.String("prompt_surface_classes", tkJoinPromptSurfaceClasses(fp.PromptSurfaceClasses)),
		slog.String("reminder_date_line_class", fp.ReminderDateLineClass),
		slog.Bool("geo_stego_canonical", fp.GeoStegoCanonical),
		slog.String("surface_signature", fp.SurfaceSignature),
		slog.String("normalize_changes", strings.Join(parts, ",")),
	}
	if len(fp.UnknownSurfaces) > 0 {
		attrs = append(attrs, slog.String("unknown_surfaces", strings.Join(fp.UnknownSurfaces, ",")))
	}
	slog.Info(tkPromptFingerprintLogKey, attrs...)
}

func tkJoinPromptSurfaceClasses(classes []tkCCPromptSurfaceClass) string {
	parts := make([]string, 0, len(classes))
	for _, cls := range classes {
		parts = append(parts, string(cls))
	}
	return strings.Join(parts, ",")
}

func promptSurfaceClassesContain(classes []tkCCPromptSurfaceClass, want tkCCPromptSurfaceClass) bool {
	for _, cls := range classes {
		if cls == want {
			return true
		}
	}
	return false
}

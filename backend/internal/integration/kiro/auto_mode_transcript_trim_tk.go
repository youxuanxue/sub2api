package kiro

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Auto-mode transcript trim prototype.
// Contract: docs/approved/kiro-auto-mode-transcript-trim.md (pending until human approval).
// Default callers must leave Extra unset; this package never enables itself.

const (
	autoModeTranscriptOpen  = "<transcript>"
	autoModeTranscriptClose = "</transcript>"
	autoModeTrimPlaceholder = "[Older tool-process records were omitted to fit the model's input limit.]"
)

// AutoModeTranscriptTrimReport is size/count only — never request body text.
type AutoModeTranscriptTrimReport struct {
	Candidate          bool   `json:"candidate"`
	Applied            bool   `json:"applied"`
	SkipReason         string `json:"skip_reason,omitempty"`
	Shape              string `json:"shape,omitempty"` // "ndjson" | "content_blocks"
	RecordsBefore      int    `json:"records_before,omitempty"`
	RecordsAfter       int    `json:"records_after,omitempty"`
	ToolProcessRemoved int    `json:"tool_process_removed,omitempty"`
	MetaRemoved        int    `json:"meta_removed,omitempty"`
	Rounds             int    `json:"rounds,omitempty"`
}

type transcriptRecordKind int

const (
	recordKindUnknown transcriptRecordKind = iota
	recordKindUser
	recordKindLatestAction
	recordKindToolProcess
	recordKindMeta
)

type transcriptRecord struct {
	raw  string
	kind transcriptRecordKind
}

type transcriptParse struct {
	shape    string
	prefix   string
	suffix   string
	records  []transcriptRecord
	msgIndex int
	// blockSpan is set for content_blocks shape: inclusive indices of open/close
	// marker blocks inside the user message content slice.
	blockOpen  int
	blockClose int
	blocks     []ClaudeContentBlock
}

// DetectClaudeAutoModeTranscriptCandidate reports whether req looks like a
// Claude Code auto-mode classifier request. Detection alone never authorizes
// mutation — parse must also succeed.
func DetectClaudeAutoModeTranscriptCandidate(req *ClaudeRequest) bool {
	if req == nil {
		return false
	}
	if req.MaxTokens <= 0 || req.MaxTokens > 64 {
		return false
	}
	if len(req.Tools) > 0 {
		return false
	}
	joined := joinClaudeUserText(req)
	if !strings.Contains(joined, autoModeTranscriptOpen) || !strings.Contains(joined, autoModeTranscriptClose) {
		return false
	}
	open := strings.Index(joined, autoModeTranscriptOpen)
	closeIdx := strings.LastIndex(joined, autoModeTranscriptClose)
	return open >= 0 && closeIdx > open
}

// MaybeTrimClaudeAutoModeTranscript removes every removable old tool-process
// record in one pass (no size budget). Prefer TrimClaudeAutoModeTranscriptUntil
// on the overflow path so deletions stop once the payload fits.
func MaybeTrimClaudeAutoModeTranscript(req *ClaudeRequest) AutoModeTranscriptTrimReport {
	return TrimClaudeAutoModeTranscriptUntil(req, func(*ClaudeRequest) bool { return true })
}

// TrimClaudeAutoModeTranscriptUntil deletes oldest tool-process records (then
// oldest non-auth meta) one at a time while stillOver(req) is true. stillOver
// should re-translate and measure; on parse/role failure the request is left
// unchanged. When stillOver is nil, behaves like a single full tool-process drain.
func TrimClaudeAutoModeTranscriptUntil(req *ClaudeRequest, stillOver func(*ClaudeRequest) bool) AutoModeTranscriptTrimReport {
	rep := AutoModeTranscriptTrimReport{}
	if req == nil {
		rep.SkipReason = "nil_request"
		return rep
	}
	if !DetectClaudeAutoModeTranscriptCandidate(req) {
		rep.SkipReason = "not_candidate"
		return rep
	}
	rep.Candidate = true

	parsed, skip := parseAutoModeTranscript(req)
	if skip != "" {
		rep.SkipReason = skip
		return rep
	}
	rep.Shape = parsed.shape
	rep.RecordsBefore = len(parsed.records)

	if stillOver == nil {
		stillOver = func(*ClaudeRequest) bool { return true }
	}
	// Only mutate when the caller says we are still over the budget.
	if !stillOver(req) {
		rep.SkipReason = "already_fits"
		return rep
	}

	working := append([]transcriptRecord(nil), parsed.records...)
	classifyTranscriptRecords(working)
	if !hasLatestAction(working) {
		rep.SkipReason = "missing_latest_action"
		return rep
	}
	if anyUnknown(working) {
		rep.SkipReason = "unknown_record_role"
		return rep
	}

	placeholderInserted := false
	for stillOver(req) {
		kind, idx := nextRemovableIndex(working)
		if idx < 0 {
			if rep.ToolProcessRemoved == 0 && rep.MetaRemoved == 0 {
				rep.SkipReason = "nothing_removable"
			} else {
				rep.SkipReason = "still_over_after_trim"
			}
			break
		}
		working = append(working[:idx], working[idx+1:]...)
		switch kind {
		case recordKindToolProcess:
			rep.ToolProcessRemoved++
		case recordKindMeta:
			rep.MetaRemoved++
		}
		rep.Rounds++
		if !placeholderInserted {
			placeholderInserted = true
		}
		if !writeTranscriptParse(req, parsed, working, placeholderInserted) {
			rep.SkipReason = "replace_failed"
			rep.Applied = false
			return rep
		}
		rep.Applied = true
		rep.RecordsAfter = len(working)
	}
	if rep.Applied && stillOver(req) && rep.SkipReason == "" {
		rep.SkipReason = "still_over_after_trim"
	}
	if rep.Applied && rep.SkipReason == "" {
		rep.RecordsAfter = len(working)
	}
	return rep
}

func nextRemovableIndex(records []transcriptRecord) (transcriptRecordKind, int) {
	for i, r := range records {
		if r.kind == recordKindToolProcess {
			return recordKindToolProcess, i
		}
	}
	for i, r := range records {
		if r.kind == recordKindMeta {
			return recordKindMeta, i
		}
	}
	return recordKindUnknown, -1
}

func parseAutoModeTranscript(req *ClaudeRequest) (*transcriptParse, string) {
	for i, msg := range req.Messages {
		if !strings.EqualFold(msg.Role, "user") {
			continue
		}
		if blocks, ok := asClaudeContentBlocks(msg.Content); ok {
			if p, skip := parseTranscriptFromBlocks(blocks); p != nil {
				p.msgIndex = i
				p.blocks = blocks
				return p, ""
			} else if skip != "" && strings.Contains(strings.Join(claudeMessagePlainText(msg.Content), "\n"), autoModeTranscriptOpen) {
				return nil, skip
			}
		}
		joined := strings.Join(claudeMessagePlainText(msg.Content), "\n")
		if !strings.Contains(joined, autoModeTranscriptOpen) {
			continue
		}
		body, prefix, suffix, ok := extractTranscriptInterior(joined)
		if !ok {
			return nil, "unbalanced_transcript_markers"
		}
		records, err := parseTranscriptNDJSON(body)
		if err != nil {
			return nil, "parse_failed"
		}
		return &transcriptParse{
			shape:    "ndjson",
			prefix:   prefix,
			suffix:   suffix,
			records:  records,
			msgIndex: i,
		}, ""
	}
	return nil, "transcript_message_not_found"
}

func parseTranscriptFromBlocks(blocks []ClaudeContentBlock) (*transcriptParse, string) {
	openIdx, closeIdx := -1, -1
	for i, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if openIdx < 0 && strings.Contains(b.Text, autoModeTranscriptOpen) {
			openIdx = i
		}
		if strings.Contains(b.Text, autoModeTranscriptClose) {
			closeIdx = i
		}
	}
	if openIdx < 0 || closeIdx < openIdx {
		return nil, ""
	}
	// Single-block transcript: fall through to NDJSON on that block's interior.
	if openIdx == closeIdx {
		body, prefix, suffix, ok := extractTranscriptInterior(blocks[openIdx].Text)
		if !ok {
			return nil, "unbalanced_transcript_markers"
		}
		records, err := parseTranscriptNDJSON(body)
		if err != nil {
			return nil, "parse_failed"
		}
		return &transcriptParse{
			shape:      "ndjson",
			prefix:     prefix,
			suffix:     suffix,
			records:    records,
			blockOpen:  openIdx,
			blockClose: closeIdx,
		}, ""
	}
	// Multi-block: open and close must be marker-only (or marker + whitespace);
	// interior blocks are whole records.
	if !blockIsMarkerOnly(blocks[openIdx].Text, autoModeTranscriptOpen) ||
		!blockIsMarkerOnly(blocks[closeIdx].Text, autoModeTranscriptClose) {
		return nil, "parse_failed"
	}
	if closeIdx-openIdx < 2 {
		return nil, "parse_failed"
	}
	records := make([]transcriptRecord, 0, closeIdx-openIdx-1)
	for _, b := range blocks[openIdx+1 : closeIdx] {
		if b.Type != "text" {
			return nil, "parse_failed"
		}
		blockRecords, err := parseTranscriptNDJSON(b.Text)
		if err != nil {
			return nil, "parse_failed"
		}
		records = append(records, blockRecords...)
	}
	if len(records) == 0 {
		return nil, "parse_failed"
	}
	return &transcriptParse{
		shape:      "content_blocks",
		records:    records,
		blockOpen:  openIdx,
		blockClose: closeIdx,
	}, ""
}

func blockIsMarkerOnly(text, marker string) bool {
	trim := strings.TrimSpace(text)
	return trim == marker
}

func writeTranscriptParse(req *ClaudeRequest, parsed *transcriptParse, records []transcriptRecord, withPlaceholder bool) bool {
	if parsed == nil || req == nil || parsed.msgIndex < 0 || parsed.msgIndex >= len(req.Messages) {
		return false
	}
	switch parsed.shape {
	case "content_blocks":
		if len(parsed.blocks) == 0 {
			return false
		}
		out := make([]ClaudeContentBlock, 0, parsed.blockOpen+2+len(records)+1+(len(parsed.blocks)-parsed.blockClose))
		out = append(out, parsed.blocks[:parsed.blockOpen]...)
		out = append(out, ClaudeContentBlock{Type: "text", Text: autoModeTranscriptOpen})
		if withPlaceholder {
			out = append(out, ClaudeContentBlock{Type: "text", Text: autoModeTrimPlaceholder})
		}
		for _, rec := range records {
			out = append(out, ClaudeContentBlock{Type: "text", Text: rec.raw})
		}
		out = append(out, ClaudeContentBlock{Type: "text", Text: autoModeTranscriptClose})
		out = append(out, parsed.blocks[parsed.blockClose+1:]...)
		req.Messages[parsed.msgIndex].Content = out
		parsed.blocks = out
		for i, b := range out {
			if b.Text == autoModeTranscriptOpen {
				parsed.blockOpen = i
			}
			if b.Text == autoModeTranscriptClose {
				parsed.blockClose = i
			}
		}
		return true
	default:
		var b strings.Builder
		_, _ = b.WriteString(parsed.prefix)
		_, _ = b.WriteString(autoModeTranscriptOpen)
		_ = b.WriteByte('\n')
		if withPlaceholder {
			_, _ = b.WriteString(autoModeTrimPlaceholder)
			_ = b.WriteByte('\n')
		}
		for _, rec := range records {
			_, _ = b.WriteString(rec.raw)
			_ = b.WriteByte('\n')
		}
		_, _ = b.WriteString(autoModeTranscriptClose)
		_, _ = b.WriteString(parsed.suffix)
		req.Messages[parsed.msgIndex].Content = []ClaudeContentBlock{{Type: "text", Text: b.String()}}
		return true
	}
}

func joinClaudeUserText(req *ClaudeRequest) string {
	var parts []string
	for _, msg := range req.Messages {
		if !strings.EqualFold(msg.Role, "user") {
			continue
		}
		parts = append(parts, claudeMessagePlainText(msg.Content)...)
	}
	return strings.Join(parts, "\n")
}

func asClaudeContentBlocks(content any) ([]ClaudeContentBlock, bool) {
	switch v := content.(type) {
	case []ClaudeContentBlock:
		return v, true
	case []any:
		out := make([]ClaudeContentBlock, 0, len(v))
		for _, raw := range v {
			switch block := raw.(type) {
			case ClaudeContentBlock:
				out = append(out, block)
			case map[string]any:
				b := ClaudeContentBlock{}
				if typ, _ := block["type"].(string); typ != "" {
					b.Type = typ
				}
				if text, _ := block["text"].(string); text != "" {
					b.Text = text
				}
				out = append(out, b)
			default:
				return nil, false
			}
		}
		return out, true
	default:
		return nil, false
	}
}

func claudeMessagePlainText(content any) []string {
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []ClaudeContentBlock:
		out := make([]string, 0, len(v))
		for _, block := range v {
			if block.Type == "text" && block.Text != "" {
				out = append(out, block.Text)
			}
		}
		return out
	case []any:
		blocks, ok := asClaudeContentBlocks(v)
		if !ok {
			return nil
		}
		return claudeMessagePlainText(blocks)
	default:
		return nil
	}
}

func extractTranscriptInterior(joined string) (body, prefix, suffix string, ok bool) {
	open := strings.Index(joined, autoModeTranscriptOpen)
	closeIdx := strings.LastIndex(joined, autoModeTranscriptClose)
	if open < 0 || closeIdx <= open {
		return "", "", "", false
	}
	inner := joined[open+len(autoModeTranscriptOpen) : closeIdx]
	if strings.Contains(inner, autoModeTranscriptOpen) || strings.Contains(inner, autoModeTranscriptClose) {
		return "", "", "", false
	}
	prefix = joined[:open]
	suffix = joined[closeIdx+len(autoModeTranscriptClose):]
	return inner, prefix, suffix, true
}

func parseTranscriptNDJSON(body string) ([]transcriptRecord, error) {
	lines := strings.Split(body, "\n")
	out := make([]transcriptRecord, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimRightFunc(line, unicode.IsSpace)
		if trim == "" {
			continue
		}
		if !json.Valid([]byte(trim)) {
			return nil, errTranscriptParse
		}
		out = append(out, transcriptRecord{raw: trim})
	}
	if len(out) == 0 {
		return nil, errTranscriptParse
	}
	return out, nil
}

var errTranscriptParse = errAutoModeTranscriptParse{}

type errAutoModeTranscriptParse struct{}

func (errAutoModeTranscriptParse) Error() string { return "auto-mode transcript parse failed" }

func classifyTranscriptRecords(records []transcriptRecord) {
	for i := range records {
		records[i].kind = classifyOneTranscriptRecord(records[i].raw, i == len(records)-1)
	}
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].kind == recordKindLatestAction || records[i].kind == recordKindToolProcess {
			records[i].kind = recordKindLatestAction
			if i > 0 && records[i-1].kind == recordKindMeta {
				records[i-1].kind = recordKindLatestAction
			}
			break
		}
	}
}

func classifyOneTranscriptRecord(raw string, isLast bool) transcriptRecordKind {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return recordKindUnknown
	}
	// Claude Code compact transcripts use single-key records such as
	// {"user": ...} and {"Bash": ...}, rather than the typed records used by
	// the classifier transcript. Keep this allowlist narrow: an unrecognised
	// top-level key remains unknown and therefore fail-closed.
	if len(probe) == 1 {
		for key := range probe {
			switch strings.ToLower(key) {
			case "user":
				return recordKindUser
			case "meta":
				return recordKindMeta
			case "bash", "askuserquestion", "workflow", "monitor", "edit", "write", "taskstop":
				return recordKindToolProcess
			}
		}
	}
	role := strings.ToLower(jsonRawString(probe["role"]))
	typ := strings.ToLower(jsonRawString(probe["type"]))
	name := strings.ToLower(jsonRawString(probe["name"]))
	tool := strings.ToLower(jsonRawString(probe["tool"]))

	switch {
	case role == "user" || typ == "user" || typ == "human":
		return recordKindUser
	case typ == "action" || typ == "pending_action" || (isLast && (typ == "tool_use" || typ == "assistant")):
		return recordKindLatestAction
	case typ == "tool_use" || typ == "tool_result" || typ == "tool" ||
		strings.Contains(typ, "bash") || strings.Contains(typ, "tool") ||
		name != "" && isToolishName(name) || tool != "" && isToolishName(tool):
		return recordKindToolProcess
	case typ == "meta" || typ == "outcome" || typ == "system" || role == "system":
		return recordKindMeta
	case role == "assistant" || typ == "assistant":
		return recordKindMeta
	default:
		return recordKindUnknown
	}
}

func isToolishName(name string) bool {
	switch name {
	case "bash", "read", "write", "edit", "multiedit", "glob", "grep", "task":
		return true
	default:
		return false
	}
}

func jsonRawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

func hasLatestAction(records []transcriptRecord) bool {
	for _, r := range records {
		if r.kind == recordKindLatestAction {
			return true
		}
	}
	return false
}

func anyUnknown(records []transcriptRecord) bool {
	for _, r := range records {
		if r.kind == recordKindUnknown {
			return true
		}
	}
	return false
}

package service

import (
	"encoding/json"
	"regexp"
	"strings"
)

var claudeCodeBetaTokenPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

// ClaudeCodeHTTPMimicryManifest is the settings.claude_code_http_mimicry_manifest JSON shape.
type ClaudeCodeHTTPMimicryManifest struct {
	SchemaVersion int      `json:"schema_version"`
	CCVersion     string   `json:"cc_version"`
	SonnetOpus    []string `json:"sonnet_opus"`
	Haiku         []string `json:"haiku"`
}

// ParseClaudeCodeHTTPMimicryManifest validates and normalizes manifest JSON.
// Returns nil when value is empty or invalid (caller falls back to compile-time).
func ParseClaudeCodeHTTPMimicryManifest(raw string) *ClaudeCodeHTTPMimicryManifest {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var m ClaudeCodeHTTPMimicryManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	if m.SchemaVersion < 1 {
		return nil
	}
	sonnet := normalizeMimicryBetaTokens(m.SonnetOpus)
	haiku := normalizeMimicryBetaTokens(m.Haiku)
	if len(sonnet) == 0 || len(haiku) == 0 {
		return nil
	}
	if v := NormalizeClaudeCodeUserAgentVersion(m.CCVersion); v == "" {
		return nil
	}
	m.SonnetOpus = sonnet
	m.Haiku = haiku
	m.CCVersion = NormalizeClaudeCodeUserAgentVersion(m.CCVersion)
	return &m
}

func normalizeMimicryBetaTokens(tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	out := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" || !claudeCodeBetaTokenPattern.MatchString(t) {
			return nil
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

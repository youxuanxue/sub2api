package kiro

import (
	"fmt"
	"sort"
	"strings"
)

// ErrKiroToolNameCollision reports two distinct client tool names that
// sanitizeToolName + shortenToolName normalize to the same Kiro tool name.
type ErrKiroToolNameCollision struct {
	First      string
	Second     string
	Normalized string
}

func (e *ErrKiroToolNameCollision) Error() string {
	return fmt.Sprintf(
		"tool names %q and %q both normalize to %q; Kiro tool names are camelCase and cannot distinguish them, rename one of the tools",
		e.First, e.Second, e.Normalized,
	)
}

// ValidateClaudeToolNames rejects a request whose tool names collide after
// Kiro normalization, before any part of it is rewritten.
//
// Why reject instead of disambiguating: convertClaudeTools writes
// nameMap[normalized] = original, so the second of two colliding tools wins.
// The upstream then receives two specs with the identical name, and the
// response path (client.go's OnToolUse wrapper) can only map that name back to
// one original. A client that declared both `Read` and `read` can therefore be
// handed a tool_use for a name it never declared. Appending a suffix would
// instead advertise a tool name the client cannot execute. Both failure modes
// are silent, so the request fails loudly instead.
//
// Collision is realistic rather than contrived: Claude Code's built-in `Read`
// plus any MCP server exposing `read` is enough, as is `my_tool` beside
// `myTool`.
//
// Mirrors aliasOpenAIOAuthReservedToolNames in
// internal/service/openai_codex_tool_names.go: validate every declaration
// first, mutate nothing on conflict.
func ValidateClaudeToolNames(tools []ClaudeTool) error {
	owners := make(map[string]string, len(tools))
	for _, tool := range tools {
		if err := recordKiroToolName(owners, tool.Name); err != nil {
			return err
		}
	}
	return nil
}

// ValidateOpenAIToolNames is the OpenAI-shaped counterpart of
// ValidateClaudeToolNames. convertOpenAITools shares the same nameMap shape and
// therefore the same last-writer-wins collision.
func ValidateOpenAIToolNames(tools []OpenAITool) error {
	owners := make(map[string]string, len(tools))
	for _, tool := range tools {
		if !strings.EqualFold(strings.TrimSpace(tool.Type), "function") {
			continue
		}
		if strings.TrimSpace(tool.Function.Name) == "" {
			// convertOpenAITools skips unusable specs; do not fail on them here.
			continue
		}
		if err := recordKiroToolName(owners, tool.Function.Name); err != nil {
			return err
		}
	}
	return nil
}

func recordKiroToolName(owners map[string]string, name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	normalized := shortenToolName(sanitizeToolName(name))
	previous, exists := owners[normalized]
	if exists && previous != name {
		// Stable ordering keeps the message identical regardless of tool order.
		first, second := previous, name
		if sort.StringsAreSorted([]string{second, first}) {
			first, second = second, first
		}
		return &ErrKiroToolNameCollision{First: first, Second: second, Normalized: normalized}
	}
	owners[normalized] = name
	return nil
}

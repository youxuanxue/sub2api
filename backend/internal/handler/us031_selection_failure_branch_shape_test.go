//go:build unit

package handler

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// US-031 — Bug B-10 regression detector.
//
// Messages and ChatCompletions selection-failure branches used to nest
// `if err != nil` inside `if err != nil`, plus implicit fall-through that
// mis-attributed errors as "No available accounts" in logs. The fix
// restructures both into explicit per-branch returns. This test asserts the
// bad pattern does not return (mechanical OPC enforcement: turn the human
// review observation into a machine check).
//
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-10.

func TestUS031_OpenAIMessages_SelectionFailureBranch_NoNestedRedundantIfErr(t *testing.T) {
	src := us031ReadHandlerFile(t, "openai_gateway_handler.go")
	body := us031ExtractBetween(t, src, "openai_messages.account_select_failed", "if selection == nil || selection.Account == nil")
	count := us031CountSubstring(body, "if err != nil")
	if count > 1 {
		t.Errorf("openai_gateway_handler.go::Messages must have exactly 1 `if err != nil` in the account_select_failed → selection-nil-check region, found %d (regression of Bug B-10 nested-redundant pattern)", count)
	}
}

func TestUS031_OpenAIChatCompletions_SelectionFailureBranch_NoNestedRedundantIfErr(t *testing.T) {
	src := us031ReadHandlerFile(t, "openai_chat_completions.go")
	// ChatCompletions is allowed to have ONE inner `if err != nil` after the
	// fallback-to-default-model attempt (it's the post-fallback err check, a
	// real fork point, not the redundant outer-tautology pattern). So we
	// allow at most 2 in the whole region (1 outer + 1 post-fallback).
	body := us031ExtractBetween(t, src, "openai_chat_completions.account_select_failed", "if selection == nil || selection.Account == nil")
	count := us031CountSubstring(body, "if err != nil")
	if count > 2 {
		t.Errorf("openai_chat_completions.go::ChatCompletions selection-failure region must have at most 2 `if err != nil` (outer + post-fallback), found %d (regression of Bug B-10)", count)
	}
}

func TestUS031_OpenAIMessages_SelectionFailureBranch_HasExplicitReturnPerBranch(t *testing.T) {
	src := us031ReadHandlerFile(t, "openai_gateway_handler.go")
	body := us031ExtractBetween(t, src, "openai_messages.account_select_failed", "if selection == nil || selection.Account == nil")
	// After Bug B-10 fix the Messages branch returns explicitly per-branch:
	// at least 2 `return` statements inside the if err != nil region (one
	// for failedAccountIDs==0, one for the failover-exhausted else).
	returns := us031CountSubstring(body, "return\n")
	if returns < 2 {
		t.Errorf("openai_gateway_handler.go::Messages selection-failure region must contain ≥2 explicit returns; found %d (Bug B-10 fix means each branch returns immediately rather than falling through)", returns)
	}
}

// --- helpers --------------------------------------------------------------

func us031ReadHandlerFile(t *testing.T, filename string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	bs, err := os.ReadFile(filepath.Join(dir, filename))
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	return string(bs)
}

func us031ExtractBetween(t *testing.T, src, start, end string) string {
	t.Helper()
	si := strings.Index(src, start)
	if si < 0 {
		t.Fatalf("start anchor %q not found", start)
	}
	rest := src[si:]
	ei := strings.Index(rest, end)
	if ei < 0 {
		t.Fatalf("end anchor %q not found after start", end)
	}
	return rest[:ei]
}

func us031CountSubstring(s, sub string) int {
	if sub == "" {
		return 0
	}
	count := 0
	for {
		idx := strings.Index(s, sub)
		if idx < 0 {
			break
		}
		count++
		s = s[idx+len(sub):]
	}
	return count
}

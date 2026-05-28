package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// claudeDesktopGatewayUAPattern matches Claude Desktop (Electron) traffic via a custom
// inference gateway (3p). Captured 2026-05-28: UA contains "Claude/<semver>" and "Electron/".
var claudeDesktopGatewayUAPattern = regexp.MustCompile(`(?i)Claude/\d+\.\d+(?:\.\d+)?.*Electron/`)

// IsClaudeDesktopGatewayUserAgent reports whether ua looks like Claude Desktop 3p gateway traffic.
func IsClaudeDesktopGatewayUserAgent(ua string) bool {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return false
	}
	// Claude Code CLI uses claude-cli/* — never treat as Desktop.
	if claudeCodeUAPattern.MatchString(ua) {
		return false
	}
	return claudeDesktopGatewayUAPattern.MatchString(ua)
}

// IsClaudeDesktopGatewayClient reads the Desktop 3p gateway client flag from context.
func IsClaudeDesktopGatewayClient(ctx context.Context) bool {
	if v, ok := ctx.Value(ctxkey.IsClaudeDesktopGatewayClient).(bool); ok {
		return v
	}
	return false
}

// SetClaudeDesktopGatewayClient stores the Desktop 3p gateway client flag in context.
func SetClaudeDesktopGatewayClient(ctx context.Context, isDesktop bool) context.Context {
	return context.WithValue(ctx, ctxkey.IsClaudeDesktopGatewayClient, isDesktop)
}

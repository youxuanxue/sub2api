package claude

import "context"

// ClaudeCodeMimicryBetasResolver returns runtime OAuth mimicry beta lists from
// settings (when configured). ok=false means use compile-time defaults.
type ClaudeCodeMimicryBetasResolver func(ctx context.Context) (sonnetOpus, haiku []string, ok bool)

var claudeCodeMimicryBetasResolver ClaudeCodeMimicryBetasResolver

// SetClaudeCodeMimicryBetasResolver registers the runtime resolver (normally
// SettingService.GetClaudeCodeMimicryBetas).
func SetClaudeCodeMimicryBetasResolver(r ClaudeCodeMimicryBetasResolver) {
	claudeCodeMimicryBetasResolver = r
}

// GetFullClaudeCodeMimicryBetasForContext returns Sonnet/Opus OAuth mimicry
// betas: runtime manifest when set, else FullClaudeCodeMimicryBetas().
func GetFullClaudeCodeMimicryBetasForContext(ctx context.Context) []string {
	if claudeCodeMimicryBetasResolver != nil && ctx != nil {
		if sonnet, _, ok := claudeCodeMimicryBetasResolver(ctx); ok && len(sonnet) > 0 {
			return append([]string(nil), sonnet...)
		}
	}
	return FullClaudeCodeMimicryBetas()
}

// GetFullClaudeCodeHaikuMimicryBetasForContext returns Haiku OAuth mimicry
// betas: runtime manifest when set, else FullClaudeCodeHaikuMimicryBetas().
func GetFullClaudeCodeHaikuMimicryBetasForContext(ctx context.Context) []string {
	if claudeCodeMimicryBetasResolver != nil && ctx != nil {
		if _, haiku, ok := claudeCodeMimicryBetasResolver(ctx); ok && len(haiku) > 0 {
			return append([]string(nil), haiku...)
		}
	}
	return FullClaudeCodeHaikuMimicryBetas()
}

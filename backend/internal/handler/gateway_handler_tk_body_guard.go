package handler

// TokenKey: upstream body-size soft guard.
//
// Rule §5 (CLAUDE.md): keep upstream-shaped gateway handlers thin. Every entry
// (anthropic /v1/messages, openai /v1/chat/completions, openai /v1/responses,
// gemini v1beta, openai /v1/embeddings + /v1/images/*) calls TkEvalBodyGuard
// once after the model is parsed and before SelectAccount — that is the
// natural choke point where we know (a) the body size, (b) the resolved
// model, and (c) the platform we are about to forward to. The full decision
// logic lives in this file so the upstream-shaped entry files stay 1-line
// hooks.
//
// Why this exists (and why it ships DEFAULT OFF): pre-flighting on client byte
// count is the wrong proxy metric. The original 900 KB reject came from a
// single 2026-05-20 edge:us1 observation (PR #322) of claude-opus-4-7 upstream
// 403s on bodies >= ~940 KB. Re-validated 2026-06-13: opus-4-7 AND opus-4-8
// both serve >1 MB requests (tested up to 1.03 MB / ~317K input tokens) with
// HTTP 200 on the current fleet — the ~940 KB 403 cliff no longer reproduces.
// Per Anthropic, oversize is 413 @ 32 MB and 403 is permission/WAF-class, so
// the old 403 was a WAF/datacenter-IP edge condition, not a stable size limit.
// So TK injects NO default guard; a returning size cliff is caught reactively
// via the upstream's real reject codes (403/413) surfaced in ops_error_logs /
// edge-health. This mechanism is retained as an OPT-IN operator tool: it is a
// pure no-op until an operator configures gateway.upstream_body_guards
// per-(platform, model_prefix) in yaml.
//
// Contract:
//   - Function does NOT write a response — each platform uses its own error
//     JSON schema (anthropic vs openai vs gemini), so callers wire the
//     returned (reject, msg) into their own errorResponse / googleError
//     helper.
//   - First matching rule wins; later rules are ignored once one matches.
//   - Empty/zero thresholds disable that side (warn_bytes<=0 disables warn;
//     reject_bytes<=0 disables reject — letting a rule be "observe-only").

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"go.uber.org/zap"
)

// TkEvalBodyGuard evaluates the configured upstream body-size guards against
// the current request. Returns (reject=true, msg) when callers must
// short-circuit with a 413; otherwise returns (false, "") and may have
// emitted an INFO "gateway.body_size_warn" log along the way.
//
// log may be nil (no log emitted then). platform is compared
// case-insensitively after trimming; model is matched via HasPrefix on
// guard.ModelPrefix (empty prefix matches the whole platform).
func TkEvalBodyGuard(
	log *zap.Logger,
	guards []config.UpstreamBodyGuardConfig,
	platform, model string,
	bodyLen int,
) (reject bool, rejectMsg string) {
	if bodyLen <= 0 || len(guards) == 0 {
		return false, ""
	}
	platformKey := strings.ToLower(strings.TrimSpace(platform))
	if platformKey == "" {
		return false, ""
	}
	modelTrim := strings.TrimSpace(model)

	for _, g := range guards {
		if strings.ToLower(strings.TrimSpace(g.Platform)) != platformKey {
			continue
		}
		prefix := strings.TrimSpace(g.ModelPrefix)
		if prefix != "" && !strings.HasPrefix(modelTrim, prefix) {
			continue
		}
		// First matching rule wins.
		if g.RejectBytes > 0 && int64(bodyLen) > g.RejectBytes {
			if log != nil {
				log.Info("gateway.body_size_reject",
					zap.String("platform", platformKey),
					zap.String("model", modelTrim),
					zap.Int("body_bytes", bodyLen),
					zap.Int64("reject_bytes", g.RejectBytes),
					zap.String("model_prefix", prefix),
				)
			}
			return true, buildBodyGuardRejectMessage(bodyLen, modelTrim, g.RejectBytes)
		}
		if g.WarnBytes > 0 && int64(bodyLen) > g.WarnBytes {
			if log != nil {
				log.Info("gateway.body_size_warn",
					zap.String("platform", platformKey),
					zap.String("model", modelTrim),
					zap.Int("body_bytes", bodyLen),
					zap.Int64("warn_bytes", g.WarnBytes),
					zap.Int64("reject_bytes", g.RejectBytes),
					zap.String("model_prefix", prefix),
				)
			}
		}
		return false, ""
	}
	return false, ""
}

// buildBodyGuardRejectMessage formats a single-line, actionable hint for
// claude-cli / openai-cli / sdk users. The message intentionally names the
// concrete limit so operators can spot which guard rule fired by looking at
// the response alone.
func buildBodyGuardRejectMessage(bodyLen int, model string, limit int64) string {
	if model == "" {
		model = "(unknown)"
	}
	return fmt.Sprintf(
		"Request body %d bytes for model %q exceeded TokenKey's pre-flight limit of %d bytes for this model. "+
			"This upstream often rejects oversized requests with HTTP 403; reduce body size with /compact or by starting a new conversation.",
		bodyLen, model, limit,
	)
}

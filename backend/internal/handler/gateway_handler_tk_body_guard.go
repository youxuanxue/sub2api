package handler

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

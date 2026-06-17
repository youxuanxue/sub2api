package service

import "github.com/tidwall/gjson"

// tkThinkingModeActiveFromBody reports whether an OpenAI-compatible chat request
// runs in thinking mode, for billing purposes.
//
// We mirror the upstream's own discriminator instead of inventing a TK scheme:
// Alibaba DashScope exposes one model id (qwen3-8b/14b/32b) and switches the
// output price by the request's `enable_thinking` parameter, which DEFAULTS to
// true for the open-source dense Qwen3 models. So thinking is considered active
// UNLESS the body explicitly sets enable_thinking to false. The flag only ever
// changes billing for models that carry a ThinkingOutputPricePerToken
// (see computeTokenBreakdown); for every other model it is a no-op, so the
// default-true reading is safe to apply unconditionally on this path.
//
// enable_thinking is a non-OpenAI-standard parameter; OpenAI-SDK clients pass it
// via extra_body, which lands top-level in the wire JSON — so a top-level lookup
// is the correct (and only) place to read it.
func tkThinkingModeActiveFromBody(body []byte) bool {
	if v := gjson.GetBytes(body, "enable_thinking"); v.Exists() && !v.Bool() {
		return false
	}
	return true
}

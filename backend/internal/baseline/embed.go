// Package baseline embeds the TokenKey Anthropic tier/stub baselines into the
// backend binary so the in-process tier-apply action and the config reconciler
// can derive desired account config WITHOUT an operator laptop / SSM round-trip.
//
// Single-source discipline (CLAUDE.md §10, memory "anthropic tier baseline 单一源"):
// the JSON files here are byte-copies of the canonical sources under
// deploy/aws/stage0/. go:embed cannot reach outside the backend module, so a copy
// is unavoidable — scripts/sentinels/check-tier-baseline-embed.py asserts the copy
// stays semantically identical to deploy/aws/stage0/ in preflight + CI.
package baseline

import _ "embed"

//go:embed anthropic-oauth-stability-baselines-tiered.json
var tierBaselineJSON []byte

//go:embed anthropic-stub-pool-baselines.json
var stubPoolBaselineJSON []byte

//go:embed anthropic-http-mimicry-baselines.json
var httpMimicryBaselineJSON []byte

// RawTierBaselineJSON returns the embedded tier baseline document bytes.
// Exposed for the sentinel/test to compare against the deploy/aws/stage0 source.
func RawTierBaselineJSON() []byte { return tierBaselineJSON }

// RawStubPoolBaselineJSON returns the embedded stub-pool policy bytes.
func RawStubPoolBaselineJSON() []byte { return stubPoolBaselineJSON }

// RawHTTPMimicryBaselineJSON returns the embedded Claude Code HTTP mimicry policy
// bytes (UA version + per-model betas). Exposed for the sentinel/test.
func RawHTTPMimicryBaselineJSON() []byte { return httpMimicryBaselineJSON }

package service

import (
	"regexp"
	"strings"
	"sync"

	"github.com/tidwall/sjson"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TokenKey: bare claude family-name fallback ("opus" → latest servable opus id).
//
// Prod incident 2026-06-06 (user_id=16): clients send the bare family word and
// die with the #617 unsupported-model 400. Friendlier contract: a bare family
// name means "the best model of that family you can serve". Truth source is
// supportedAnthropicCatalogModels (pricing_catalog_supported_models_tk.go);
// families are DERIVED from the table, never hand-listed — a servable refresh
// (new opus 4.9 row, new `claude-zenith-6` family) changes the answer with
// ZERO code edits. Trigger is strictly LEXICAL-EXACT (after lower+trim and the
// existing tkStripContextWindowModelAlias pass): the model must equal a family
// word or `claude-<family>`. Everything else — full ids, dated snapshots,
// retired names like "claude-3-5-haiku-20241022" (versioned ⇒ natural miss;
// the deprecated-model interceptor keeps owning it), unknown families,
// substrings — is returned byte-identical with zero rewrites. The rewrite
// happens at the handler throat BEFORE channel mapping / session hash /
// scheduling / usage recording, so every downstream consumer (including the
// originalModel billing key) sees only the resolved full id.

// tkBareAliasFamilyPattern recognizes ids of the shape `claude-<family>(-<num>)+`:
// one lowercase family word + one or more numeric version segments. Ids with a
// numeric family position (claude-3-5-haiku-…) contribute no family.
var tkBareAliasFamilyPattern = regexp.MustCompile(`^claude-([a-z]+)((?:-\d+)+)$`)

// tkDeriveBareModelAliases derives the bare-family → latest-full-id map from a
// servable id set. Pure; families emerge from the data. Per family the winner
// has the highest version vector, compared numerically segment by segment
// (4-10 > 4-9); on a strict-prefix tie the SHORTER id wins — the undated form
// is the canonical alias (claude-haiku-4-5 outranks claude-haiku-4-5-20251001).
func tkDeriveBareModelAliases(set map[string]struct{}) map[string]string {
	type winner struct {
		id      string
		version []int
	}
	newer := func(a, b []int) bool { // numeric per-segment; shorter wins ties
		for i := 0; i < len(a) && i < len(b); i++ {
			if a[i] != b[i] {
				return a[i] > b[i]
			}
		}
		return len(a) < len(b)
	}
	best := make(map[string]winner)
	for id := range set {
		m := tkBareAliasFamilyPattern.FindStringSubmatch(id)
		if m == nil {
			continue
		}
		segs := strings.Split(strings.TrimPrefix(m[2], "-"), "-")
		version := make([]int, 0, len(segs))
		for _, s := range segs {
			n := 0
			for _, ch := range s {
				n = n*10 + int(ch-'0')
			}
			version = append(version, n)
		}
		if cur, ok := best[m[1]]; !ok || newer(version, cur.version) {
			best[m[1]] = winner{id: id, version: version}
		}
	}
	out := make(map[string]string, len(best))
	for family, w := range best {
		out[family] = w.id
	}
	return out
}

// Lazily derived once per process from the compile-time servable table.
var (
	tkBareModelAliasOnce sync.Once
	tkBareModelAliases   map[string]string
)

func tkBareModelAliasMap() map[string]string {
	tkBareModelAliasOnce.Do(func() {
		tkBareModelAliases = tkDeriveBareModelAliases(supportedAnthropicCatalogModels)
	})
	return tkBareModelAliases
}

// tkResolveBareModelAlias maps a bare family name to its latest servable full
// id. Hit requires lexical-exact equality (after lower+trim and stripping a
// trailing "[1m]"-style alias) with a family word or `claude-<family>`.
func tkResolveBareModelAlias(model string, aliases map[string]string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return "", false
	}
	if stripped, ok := tkStripContextWindowModelAlias(normalized); ok {
		normalized = stripped
	}
	resolved, ok := aliases[strings.TrimPrefix(normalized, "claude-")]
	return resolved, ok
}

// TkApplyBareModelAlias is the handler-throat entry point (Messages and
// CountTokens, right after the request model is parsed and before channel
// mapping / session hash / scheduling). Gate: anthropic path only — platform
// empty (no force-platform, no group) or PlatformAnthropic. On a hit it
// surgically rewrites ONLY the body's model field (sjson), refreshes parsed
// (body bytes + Model) and returns the new body + resolved id. On a miss — or
// any rewrite error — it returns (nil, "") with parsed untouched, so the
// caller's body stays byte-identical.
func TkApplyBareModelAlias(platform string, parsed *ParsedRequest) ([]byte, string) {
	if parsed == nil || (platform != "" && platform != PlatformAnthropic) {
		return nil, ""
	}
	// Capture the client's original bare name BEFORE the rewrite:
	// parsed.ReplaceBody re-parses the mutated body and refreshes parsed.Model,
	// so reading it afterwards would log requested==resolved and lose which
	// bare names clients actually send (the feature's only observability).
	requested := parsed.Model
	resolved, ok := tkResolveBareModelAlias(parsed.Model, tkBareModelAliasMap())
	if !ok {
		return nil, ""
	}
	newBody, err := sjson.SetBytes(parsed.Body.Bytes(), "model", resolved)
	if err != nil {
		return nil, ""
	}
	if err := parsed.ReplaceBody(newBody); err != nil {
		return nil, ""
	}
	parsed.Model = resolved
	logger.LegacyPrintf("service.gateway",
		"[Forward] bare model alias resolved family name to latest servable id before scheduling: requested=%s resolved=%s",
		requested, resolved)
	return newBody, resolved
}

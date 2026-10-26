//go:build unit

package service

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func parseAnthropicIngressBody(t *testing.T, body string) *ParsedRequest {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
	require.NoError(t, err)
	return parsed
}

// The positive case: the handler-throat strip rewrites both parsed.Model and the
// wire body, leaving every other field byte-identical.
func TestTkStripContextWindowAliasAtIngress_RewritesModelAndBody(t *testing.T) {
	for _, tc := range []struct{ name, platform, model, bare string }{
		{"anthropic sonnet", PlatformAnthropic, "claude-sonnet-5[1m]", "claude-sonnet-5"},
		{"anthropic opus", PlatformAnthropic, "claude-opus-4-8[1m]", "claude-opus-4-8"},
		{"kiro platform group", PlatformKiro, "claude-sonnet-5[1m]", "claude-sonnet-5"},
		{"universal key empty platform", "", "claude-opus-5[1m]", "claude-opus-5"},
		{"uppercase unit", PlatformAnthropic, "claude-sonnet-5[1M]", "claude-sonnet-5"},
		{"200k alias", PlatformAnthropic, "claude-sonnet-5[200k]", "claude-sonnet-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := parseAnthropicIngressBody(t,
				`{"model":"`+tc.model+`","max_tokens":1024,"metadata":{"user_id":"session_abc"},"messages":[{"role":"user","content":"hi"}]}`)

			newBody, bare := TkStripContextWindowAliasAtIngress(tc.platform, parsed)
			require.Equal(t, tc.bare, bare)

			want := []byte(`{"model":"` + tc.bare + `","max_tokens":1024,"metadata":{"user_id":"session_abc"},"messages":[{"role":"user","content":"hi"}]}`)
			require.Equal(t, string(want), string(newBody))
			require.Equal(t, tc.bare, parsed.Model)
			require.Equal(t, string(want), string(parsed.Body.Bytes()))
			// Re-parse must survive: downstream reads metadata.user_id for sticky.
			require.Equal(t, "session_abc", parsed.MetadataUserID)
		})
	}
}

// The negative direction: bare ids, non-alias bracket shapes and out-of-scope
// platforms must leave parsed byte-identical. A guard that rewrote these would
// be collateral damage on 99.99% of traffic.
func TestTkStripContextWindowAliasAtIngress_MissIsByteIdentical(t *testing.T) {
	for _, tc := range []struct{ name, platform, body string }{
		{"bare anthropic id", PlatformAnthropic, `{"model":"claude-sonnet-5","max_tokens":1}`},
		{"dated snapshot", PlatformAnthropic, `{"model":"claude-haiku-4-5-20251001","max_tokens":1}`},
		{"bare family word", PlatformAnthropic, `{"model":"opus","max_tokens":1}`},
		{"bracket without unit letter", PlatformAnthropic, `{"model":"claude-sonnet-5[1]","max_tokens":1}`},
		{"bracket mid-string", PlatformAnthropic, `{"model":"claude-[1m]-preview","max_tokens":1}`},
		{"empty model", PlatformAnthropic, `{"max_tokens":1}`},
		// Out-of-scope platforms: the OpenAI-compat shape owns its own strip at
		// NormalizeOpenAICompatRequestedModel / normalizeCodexModel.
		{"openai platform", PlatformOpenAI, `{"model":"gpt-5.5[1m]","max_tokens":1}`},
		{"gemini platform", PlatformGemini, `{"model":"claude-sonnet-5[1m]","max_tokens":1}`},
		{"antigravity platform", PlatformAntigravity, `{"model":"claude-sonnet-5[1m]","max_tokens":1}`},
		{"newapi platform", PlatformNewAPI, `{"model":"claude-sonnet-5[1m]","max_tokens":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := []byte(tc.body)
			parsed := parseAnthropicIngressBody(t, tc.body)
			origModel := parsed.Model

			newBody, bare := TkStripContextWindowAliasAtIngress(tc.platform, parsed)
			require.Empty(t, bare)
			require.Nil(t, newBody)
			require.Equal(t, origModel, parsed.Model)
			require.True(t, bytes.Equal(parsed.Body.Bytes(), orig),
				"parsed body mutated on miss: got=%s want=%s", parsed.Body.Bytes(), orig)
		})
	}
}

// Idempotence: the forward / passthrough / count_tokens strips still run after
// this one (project rule §5.x keeps them), so a second pass over an already-bare
// id must be a no-op rather than a second rewrite.
func TestTkStripContextWindowAliasAtIngress_Idempotent(t *testing.T) {
	parsed := parseAnthropicIngressBody(t, `{"model":"claude-sonnet-5[1m]","max_tokens":1}`)

	firstBody, firstBare := TkStripContextWindowAliasAtIngress(PlatformAnthropic, parsed)
	require.Equal(t, "claude-sonnet-5", firstBare)

	secondBody, secondBare := TkStripContextWindowAliasAtIngress(PlatformAnthropic, parsed)
	require.Empty(t, secondBare, "second pass must report no strip")
	require.Nil(t, secondBody)
	require.Equal(t, "claude-sonnet-5", parsed.Model)
	require.Equal(t, string(firstBody), string(parsed.Body.Bytes()))

	// And the forward-stage pure stripper agrees it has nothing left to do.
	again, stripped := tkStripContextWindowModelAlias(parsed.Model)
	require.False(t, stripped)
	require.Equal(t, "claude-sonnet-5", again)
}

// Ordering contract with TkApplyBareModelAlias. tkResolveBareModelAlias strips
// internally, so "sonnet[1m]" already resolved; the leaked class is a FULL id
// with the suffix, which is an alias-table miss. Running the ingress strip first
// makes both classes land on a bare, servable id.
func TestTkStripContextWindowAliasAtIngress_RunsBeforeBareModelAlias(t *testing.T) {
	t.Run("full id with alias: only the ingress strip repairs it", func(t *testing.T) {
		// Pre-fix behavior, pinned: the alias table alone cannot resolve a full id.
		aliasOnly := parseAnthropicIngressBody(t, `{"model":"claude-sonnet-5[1m]","max_tokens":1}`)
		_, resolved := TkApplyBareModelAlias(PlatformAnthropic, aliasOnly)
		require.Empty(t, resolved, "full id + [1m] is an alias-table miss — this is the leak")
		require.Equal(t, "claude-sonnet-5[1m]", aliasOnly.Model)

		// With the ingress strip in front, the model reaching selection is bare.
		parsed := parseAnthropicIngressBody(t, `{"model":"claude-sonnet-5[1m]","max_tokens":1}`)
		_, bare := TkStripContextWindowAliasAtIngress(PlatformAnthropic, parsed)
		require.Equal(t, "claude-sonnet-5", bare)
		TkApplyBareModelAlias(PlatformAnthropic, parsed)
		require.Equal(t, "claude-sonnet-5", parsed.Model)
	})

	t.Run("bare family with alias still resolves to the servable full id", func(t *testing.T) {
		parsed := parseAnthropicIngressBody(t, `{"model":"sonnet[1m]","max_tokens":1}`)
		_, bare := TkStripContextWindowAliasAtIngress(PlatformAnthropic, parsed)
		require.Equal(t, "sonnet", bare)
		_, resolved := TkApplyBareModelAlias(PlatformAnthropic, parsed)
		require.Equal(t, "claude-sonnet-5", resolved) // real-table pin
		require.Equal(t, "claude-sonnet-5", parsed.Model)
	})
}

// The defect itself: per-account model support. Pinned in both directions so the
// table is the regression, not a restatement of the implementation.
//
// Empty-mapping native Anthropic accounts always answered true (Account.
// IsModelSupported short-circuits "no mapping = allow all"), which is why the
// bug was not a fleet-wide outage. Kiro (exact catalog lookup) and explicit
// model_mapping accounts answered FALSE for the bracketed id — those requests
// never reached the forward-stage strip.
func TestAccountModelSupport_ContextWindowAliasExclusion(t *testing.T) {
	emptyMappingAnthropic := &Account{
		ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-only"},
	}
	kiro := &Account{
		ID: 2, Platform: PlatformKiro, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-only"},
		Extra:       map[string]any{"mixed_scheduling": true},
	}
	explicitMapping := &Account{
		ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":       "test-only",
			"model_mapping": map[string]any{"claude-sonnet-5": "claude-sonnet-5"},
		},
	}

	for _, tc := range []struct {
		name                            string
		account                         *Account
		bareSupported, aliasedSupported bool
	}{
		// Escaped via "no mapping = allow all" — same answer either way.
		{"empty-mapping anthropic", emptyMappingAnthropic, true, true},
		// The two broken classes: bare true, bracketed false.
		{"kiro catalog", kiro, true, false},
		{"explicit model_mapping", explicitMapping, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.bareSupported,
				accountAdmitsRequestedModel(tc.account, "claude-sonnet-5", nil),
				"bare id support")
			require.Equal(t, tc.aliasedSupported,
				accountAdmitsRequestedModel(tc.account, "claude-sonnet-5[1m]", nil),
				"bracketed id support")

			// The fix: whatever the account's verdict on the bracketed id, the model
			// that actually reaches selection is bare, so bare and aliased requests
			// now get the same answer.
			parsed := parseAnthropicIngressBody(t, `{"model":"claude-sonnet-5[1m]","max_tokens":1}`)
			_, bare := TkStripContextWindowAliasAtIngress(PlatformAnthropic, parsed)
			require.Equal(t, "claude-sonnet-5", bare)
			require.Equal(t, tc.bareSupported, accountAdmitsRequestedModel(tc.account, parsed.Model, nil))
		})
	}
}

// Amplifier 1: the 60s group negative cache. CanonicalizeOpenAICompatRoutingModel
// keeps a trailing "[1m]" (it is not a Codex id), so a bracketed model produced a
// distinct cache key — one selection failure then fast-failed the group×model
// pair for a full minute. After the ingress strip the key is the bare one.
func TestTkGroupUnsupportedModelCacheKey_ContextWindowAlias(t *testing.T) {
	const groupID = int64(7)

	aliasedKey := tkGroupUnsupportedModelCacheKey(groupID, "claude-sonnet-5[1m]")
	bareKey := tkGroupUnsupportedModelCacheKey(groupID, "claude-sonnet-5")
	require.NotEqual(t, bareKey, aliasedKey,
		"canonicalization keeps the [1m] suffix — this is the amplifier")
	require.Contains(t, aliasedKey, "[1m]")

	parsed := parseAnthropicIngressBody(t, `{"model":"claude-sonnet-5[1m]","max_tokens":1}`)
	_, bare := TkStripContextWindowAliasAtIngress(PlatformAnthropic, parsed)
	require.Equal(t, bareKey, tkGroupUnsupportedModelCacheKey(groupID, bare),
		"post-strip the negative cache keys on the bare id")

	// End to end through the cache: a bracketed request no longer stores or hits
	// a suffixed entry, and a prior bare verdict is correctly reused.
	cache := newTkGroupUnsupportedModelNegativeCacheWithTTL(0, 0)
	gid := groupID
	require.NoError(t, tkGroupUnsupportedModelShortCircuit(cache, &gid, bare))
	cache.put(gid, bare)
	require.Error(t, tkGroupUnsupportedModelShortCircuit(cache, &gid, bare))
	require.Error(t, tkGroupUnsupportedModelShortCircuit(cache, &gid, parsed.Model),
		"the stripped model reuses the bare verdict instead of starting a second key")
}

// Amplifier 2: selection-failure classification. All-model_unsupported stats
// surfaced 400 "Unsupported model: claude-sonnet-5[1m]"; mixed stats surfaced
// 429. Claude Code renders both as "temporarily unavailable" and drops auto mode.
// Post-strip the message names the real model, and the bracketed id is gone.
func TestTkWrapSelectionFailure_MessageCarriesBareModel(t *testing.T) {
	stats := selectionFailureStats{Total: 2, ModelUnsupported: 2}

	aliased := tkWrapSelectionFailure(PlatformAnthropic, "claude-sonnet-5[1m]", stats)
	require.ErrorIs(t, aliased, ErrUnsupportedModel)
	require.Contains(t, aliased.Error(), "[1m]", "pre-strip the client sees the bracketed id")

	parsed := parseAnthropicIngressBody(t, `{"model":"claude-sonnet-5[1m]","max_tokens":1}`)
	_, bare := TkStripContextWindowAliasAtIngress(PlatformAnthropic, parsed)
	stripped := tkWrapSelectionFailure(PlatformAnthropic, bare, stats)
	require.ErrorIs(t, stripped, ErrUnsupportedModel)
	require.Contains(t, stripped.Error(), "claude-sonnet-5")
	require.NotContains(t, stripped.Error(), "[1m]")
}

// Billing / sticky consistency. The pricing+quota key is derived from the model
// the handler carries forward; a bracketed id has no pricing row, so quota would
// leak on exactly the 1M requests this fix lets succeed. The sticky session hash
// must also be unaffected by the suffix — a CC session that reconnects with and
// without "[1m]" has to land on the same bound account.
func TestTkStripContextWindowAliasAtIngress_BillingAndSessionHashConsistency(t *testing.T) {
	aliased := parseAnthropicIngressBody(t, `{"model":"claude-sonnet-5[1m]","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)
	_, bare := TkStripContextWindowAliasAtIngress(PlatformAnthropic, aliased)
	require.Equal(t, "claude-sonnet-5", bare)

	bareOnly := parseAnthropicIngressBody(t, `{"model":"claude-sonnet-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)

	// originalModel / billing key is bare and carries no bracket.
	billing := forwardResultBillingModel(aliased.Model, aliased.Model)
	require.Equal(t, "claude-sonnet-5", billing)
	require.NotContains(t, billing, "[")

	// Sticky hash is identical to the equivalent bare request's.
	svc := &GatewayService{}
	require.Equal(t, svc.GenerateSessionHash(bareOnly), svc.GenerateSessionHash(aliased))
	require.NotEmpty(t, svc.GenerateSessionHash(aliased))
}

//go:build unit

package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// candidateFactsBody approximates a Codex Responses turn: a long history, a
// prompt_cache_key (which sends every Chat-only account through the
// Responses→Chat downgrade) and the routing fields at the tail.
func candidateFactsBody(bytes int) []byte {
	filler := strings.Repeat("word ", bytes/5+1)[:bytes]
	return []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` +
		filler + `"}]}],"prompt_cache_key":"pck-bench","reasoning":{"effort":"high"},"model":"gpt-5.4","stream":false}`)
}

// candidateFactsRequest builds an admission pass whose per-candidate work is
// dominated by request-only facts: groups that forbid image generation (image
// intent scan), OpenAI accounts on a Chat-shaped request (reasoning billing
// policy), and Chat-only supplies for a Responses request (downgrade).
func candidateFactsRequest(b testing.TB, accountCount, groupCount, bodyBytes int) (context.Context, *CandidateRequest) {
	b.Helper()
	groups := make([]Group, groupCount)
	groupIDs := make([]int64, groupCount)
	for i := range groups {
		id := int64(10 * (i + 1))
		groups[i] = grpNoImage(id, PlatformOpenAI, i+1, false)
		groups[i].MaxReasoningEffort = "high"
		groupIDs[i] = id
	}
	accounts := make([]Account, accountCount)
	for i := range accounts {
		accounts[i] = globalCandidateAccount(int64(i+1), 1, groupIDs...)
	}
	r, _, key := globalCandidateFixture(groups, accounts)
	body := candidateFactsBody(bodyBytes)
	request := &CandidateRequest{
		resolver: r,
		key:      key,
		groups:   groups,
		shape:    ShapeOpenAIChat,
		path:     "/v1/responses",
		model:    "gpt-5.4",
		body:     body,
	}
	request.requestProfile, request.requestProfileValid = candidateRequestProfile(request.shape, request.path, request.model, body)
	ctx := r.WithRequest(context.Background(), request.shape, request.path, request.model, body)
	if _, ok := ProtocolRoutingRequest(ctx); !ok {
		b.Fatal("benchmark request must be governed by the protocol router")
	}
	return ctx, request
}

// BenchmarkCandidateRequestFactsAdmissionPass measures one full read-only
// candidates() pass (groups, account×group evaluation, billing origin). It
// measures the existing owners and does not assert selection outcomes beyond
// requiring the fixture to produce candidates.
func BenchmarkCandidateRequestFactsAdmissionPass(b *testing.B) {
	for _, tc := range []struct{ accounts, groups, bytes int }{
		{8, 2, 65536}, {32, 4, 65536}, {32, 4, 262144},
	} {
		b.Run(fmt.Sprintf("accounts_%d/groups_%d/bytes_%d", tc.accounts, tc.groups, tc.bytes), func(b *testing.B) {
			ctx, request := candidateFactsRequest(b, tc.accounts, tc.groups, tc.bytes)
			candidates, supported, err := request.candidates(ctx, candidateSelectOptions{})
			if err != nil || !supported || len(candidates) != tc.accounts {
				b.Fatalf("fixture must admit every account: candidates=%d supported=%v err=%v", len(candidates), supported, err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Each iteration models a new request: fresh routing context and
				// selection state, so no memo survives between iterations. Ingress
				// parsing is outside the measured admission pass.
				b.StopTimer()
				ctx, request := candidateFactsRequest(b, tc.accounts, tc.groups, tc.bytes)
				b.StartTimer()
				if _, _, err := request.candidates(ctx, candidateSelectOptions{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

//go:build unit

package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Candidate selection cost is a shared data-flow problem, so a single hotspot
// percentage cannot steer the work. These benchmarks report CPU per admission
// pass over the two dimensions that actually grow in production — the account x
// group fan-out and the request body size — so the growth slope, not one
// profile sample, decides which reuse is worth building.
//
// Owner boundary: this measures the existing owners in
// docs/approved/candidate-eligibility-ssot.md. It must never assert selection
// outcomes; behavior belongs to the focused tests next to it.

// candidateCostBody approximates live Responses traffic. Routing fields stay at
// the tail so a body-size sweep also exercises the scan distance to them.
func candidateCostBody(bytes int) []byte {
	filler := strings.Repeat("word ", bytes/5+1)[:bytes]
	return []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"` +
		filler + `"}]}],"model":"gpt-5.4","stream":false}`)
}

func candidateCostRequest(accountCount, groupCount, bodyBytes int) (*CandidateRequest, []Account, []Group) {
	groups := make([]Group, groupCount)
	groupIDs := make([]int64, groupCount)
	for i := range groups {
		id := int64(10 * (i + 1))
		groups[i] = grp(id, PlatformOpenAI, i+1, false)
		groupIDs[i] = id
	}
	accounts := make([]Account, accountCount)
	for i := range accounts {
		// Every account joins every group: the admission pass then evaluates the
		// full authorized membership union, which is what production unions too.
		accounts[i] = globalCandidateAccount(int64(i+1), 1, groupIDs...)
	}
	r, _, key := globalCandidateFixture(groups, accounts)
	body := candidateCostBody(bodyBytes)
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
	return request, accounts, groups
}

// evaluateCandidateCostPass runs one read-only admission pass: the production
// per-group pathContext preparer, then every account against every group. It
// mirrors the loop in candidates() without billing, readiness or slot work, so
// the measurement isolates request-fact evaluation.
func evaluateCandidateCostPass(b *testing.B, request *CandidateRequest, accounts []Account, groups []Group) {
	prepare := candidatePathContextPreparer(request)
	for i := range accounts {
		for j := range groups {
			if _, err := request.evaluatePathWithPreparation(context.Background(), &accounts[i], &groups[j], prepare); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkCandidateAdmissionCostByFanout holds the body fixed and grows the
// account x group product. A flat ns/op per evaluation means per-candidate work
// is already shared; a rising one localizes the duplicated request work.
func BenchmarkCandidateAdmissionCostByFanout(b *testing.B) {
	const bodyBytes = 8192
	for _, fanout := range []struct{ accounts, groups int }{
		{1, 1}, {8, 1}, {32, 1}, {8, 4}, {32, 4},
	} {
		name := fmt.Sprintf("accounts_%d/groups_%d", fanout.accounts, fanout.groups)
		b.Run(name, func(b *testing.B) {
			request, accounts, groups := candidateCostRequest(fanout.accounts, fanout.groups, bodyBytes)
			evaluations := int64(fanout.accounts * fanout.groups)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				evaluateCandidateCostPass(b, request, accounts, groups)
			}
			b.StopTimer()
			// Per-evaluation cost is the comparable number across fan-outs; ns/op
			// alone scales with the product and hides the slope.
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(int64(b.N)*evaluations), "ns/eval")
		})
	}
}

// BenchmarkCandidateAdmissionCostByBodyBytes holds the fan-out fixed and grows
// the body. Cost that rises with bytes at a fixed fan-out is re-scanning of one
// immutable body, which no candidate read snapshot can remove.
func BenchmarkCandidateAdmissionCostByBodyBytes(b *testing.B) {
	const accounts, groups = 16, 2
	for _, bodyBytes := range []int{1024, 8192, 65536, 262144} {
		b.Run(fmt.Sprintf("bytes_%d", bodyBytes), func(b *testing.B) {
			request, accountList, groupList := candidateCostRequest(accounts, groups, bodyBytes)
			evaluations := int64(accounts * groups)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				evaluateCandidateCostPass(b, request, accountList, groupList)
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(int64(b.N)*evaluations), "ns/eval")
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(int64(b.N)*evaluations*int64(bodyBytes)), "ns/eval-byte")
		})
	}
}

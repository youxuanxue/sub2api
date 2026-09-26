//go:build unit

package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

// The content cache is keyed on {digest, resolvedModel}, and resolvedModel comes
// from each account's own model mapping. One request evaluated against accounts
// that map to different upstream models therefore re-runs the whole conversion
// per distinct model, over bytes that cannot change within the request. These
// benchmarks report ns/eval so the per-evaluation cost stays comparable as the
// number of distinct models grows.
func benchmarkCursorContentByModelFanout(b *testing.B, inbound protocolrouter.Protocol, models int, bodyBytes int) {
	const requested = "claude-sonnet-4-6"
	body, path := cursorFanoutBody(inbound, requested, bodyBytes)
	request, err := protocolrouter.ParseCanonicalRequest(inbound, path, requested, false, body)
	require.NoError(b, err)

	// Distinct resolved models, all anthropic-strict like a real Cursor account,
	// so every one of them takes the same branch and differs only in the key.
	resolved := make([]string, models)
	for i := range resolved {
		resolved[i] = fmt.Sprintf("claude-sonnet-4-6-v%d", i)
	}
	require.True(b, cursorProtocolContentSupported(request, resolved[0]))

	b.ReportAllocs()
	b.ResetTimer()
	evals := 0
	for i := 0; i < b.N; i++ {
		// A fresh cache per iteration matches the real scope: the cache lives in
		// the per-request routing context, so it never spans two requests.
		cache := &cursorRequestContentCache{}
		for _, model := range resolved {
			if !cache.supported(request, model) {
				b.Fatal("supported content rejected")
			}
			evals++
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(evals), "ns/eval")
}

func cursorFanoutBody(inbound protocolrouter.Protocol, model string, bodyBytes int) ([]byte, protocolrouter.ResponsesPathKind) {
	history := strings.Repeat("hello ", bodyBytes/6+1)
	if inbound == protocolrouter.ProtocolResponses {
		return []byte(fmt.Sprintf(`{"model":%q,"input":%q}`, model, history)), protocolrouter.ResponsesPathRoot
	}
	return []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}]}`, model, history)), protocolrouter.ResponsesPathNone
}

func BenchmarkCursorContentByModelFanout(b *testing.B) {
	for _, models := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("models_%d", models), func(b *testing.B) {
			benchmarkCursorContentByModelFanout(b, protocolrouter.ProtocolResponses, models, 8192)
		})
	}
}

func BenchmarkCursorContentByBodyBytes(b *testing.B) {
	// 8KiB/49KiB/205KiB bracket the measured production p50/p90/p99 body sizes.
	for _, bodyBytes := range []int{8192, 49152, 209920} {
		b.Run(fmt.Sprintf("bytes_%d", bodyBytes), func(b *testing.B) {
			benchmarkCursorContentByModelFanout(b, protocolrouter.ProtocolResponses, 4, bodyBytes)
		})
	}
}

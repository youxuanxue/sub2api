//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	pb "github.com/Wei-Shaw/sub2api/internal/integration/cursor/agentpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCursorNativeHistoryPlanAndForwardAgree(t *testing.T) {
	const model = "claude-sonnet-4-6"
	body := []byte(emulatedWebSearchBody)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, model, false, body)
	require.NoError(t, err)
	account := cursorCandidateAccount(model)
	ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
	plan, _, err := protocolPlanForAccount(ctx, account, model)
	require.NoError(t, err)
	ctx = withProtocolExecutionPlan(ctx, plan)
	var stream bytes.Buffer
	for _, msg := range []*pb.AgentServerMessage{
		{InteractionUpdate: &pb.InteractionUpdate{TextDelta: &pb.TextDeltaUpdate{Text: "OK"}}},
		{InteractionUpdate: &pb.InteractionUpdate{TurnEnded: &pb.TurnEndedUpdate{InputTokens: proto.Int64(10), OutputTokens: proto.Int64(2), CacheReadTokens: proto.Int64(0), CacheWriteTokens: proto.Int64(0)}}},
	} {
		encoded, err := proto.Marshal(msg)
		require.NoError(t, err)
		header := [5]byte{}
		binary.BigEndian.PutUint32(header[1:], uint32(len(encoded)))
		stream.Write(header[:])
		stream.Write(encoded)
	}
	upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{{StatusCode: 200, ProtoMajor: 2, Body: io.NopCloser(&stream)}}}
	c := cursorTestContext()
	svc := protocolTargetTestService(upstream)
	svc.httpUpstream = &cursorContentHTTPUpstream{upstream}
	result, err := svc.ForwardAsAnthropic(ctx, c, account, body, "", model)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, c.Writer.Status())
	require.Len(t, upstream.requests, 1, "planned native history must reach Cursor instead of a local parser 400")
	require.EqualValues(t, 2, result.Usage.OutputTokens)
	require.Equal(t, []byte(emulatedWebSearchBody), body)
	require.Equal(t, body, request.Body())
}

func TestCursorContentCacheKeepsRequestModelAndAccountBoundaries(t *testing.T) {
	const model = "claude-sonnet-4-6"
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, model, false, []byte(genuineWebSearchBody))
	require.NoError(t, err)
	cache := &cursorRequestContentCache{}
	require.True(t, cache.supported(request, "deepseek-v3.2"))
	require.False(t, cache.supported(request, model), "different resolved models cannot borrow content eligibility")
	text, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, model, false, []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	require.True(t, cache.supported(text, model), "different requests cannot inherit cached rejection")
	ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), text)
	account := cursorCandidateAccount(model)
	_, _, err = protocolPlanForAccount(ctx, account, model)
	require.NoError(t, err)
	account.Credentials["base_url"] = "https://different.example"
	_, err = protocolAccountSnapshotForRouting(ctx, account, text)
	require.ErrorIs(t, err, ErrProtocolCapabilityUnknown, "cached content cannot hide changed endpoint identity")
}

func benchmarkCursorCachedContent(b *testing.B, inbound protocolrouter.Protocol) {
	const model = "claude-sonnet-4-6"
	history := strings.Repeat("hello ", 174763)
	body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}]}`, model, history))
	path := protocolrouter.ResponsesPathNone
	if inbound == protocolrouter.ProtocolResponses {
		body = []byte(fmt.Sprintf(`{"model":%q,"input":%q}`, model, history))
		path = protocolrouter.ResponsesPathRoot
	}
	request, err := protocolrouter.ParseCanonicalRequest(inbound, path, model, false, body)
	if err != nil {
		b.Fatal(err)
	}
	ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
	account := cursorCandidateAccount(model)
	if _, _, err := protocolPlanForAccount(ctx, account, model); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := protocolPlanForAccount(ctx, account, model); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCursorCachedContentDoesNotCopyLongRequest(t *testing.T) {
	for _, inbound := range []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses} {
		t.Run(string(inbound), func(t *testing.T) {
			result := testing.Benchmark(func(b *testing.B) { benchmarkCursorCachedContent(b, inbound) })
			// Fresh account facts are small; copying the 1 MiB request on every
			// cached Plan lookup must fail even when the returned Plan is unchanged.
			require.Less(t, result.AllocedBytesPerOp(), int64(256<<10), result.String())
		})
	}
}

func BenchmarkCursorCachedContent(b *testing.B) {
	for _, inbound := range []protocolrouter.Protocol{protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses} {
		b.Run(string(inbound), func(b *testing.B) { benchmarkCursorCachedContent(b, inbound) })
	}
}

// Cursor uses a duplex request body. Return server frames without trying to
// consume its whole still-open client stream before sending a response.
type cursorContentHTTPUpstream struct{ *protocolTargetHTTPUpstream }

func (u *cursorContentHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.requests = append(u.requests, req)
	if len(u.responses) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	response := u.responses[0]
	u.responses = u.responses[1:]
	return response, nil
}

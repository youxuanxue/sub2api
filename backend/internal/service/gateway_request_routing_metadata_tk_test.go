//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func assertRoutingMetadataEquivalent(t *testing.T, body []byte) {
	t.Helper()
	before := append([]byte(nil), body...)
	metadata := gatewayRequestRoutingMetadata(body)
	for _, field := range []string{"stream", "type", "thinking.type", "reasoning.effort", "reasoning_effort", "model"} {
		old, projected := gjson.GetBytes(body, field), gjson.GetBytes(metadata, field)
		require.Equal(t, old.Type, projected.Type, field)
		require.Equal(t, old.Raw, projected.Raw, field)
		require.Equal(t, old.String(), projected.String(), field)
		require.Equal(t, old.Exists(), projected.Exists(), field)
	}
	for _, protocol := range []string{"messages", "responses", "chat_completions"} {
		require.Equal(t, gatewayRequestThinkingEnabled(body, protocol), gatewayRequestThinkingEnabled(metadata, protocol), protocol)
	}
	require.Equal(t, before, body)
}

func TestGatewayRoutingMetadataPreservesSelectorSemantics(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"input":[{"thinking":{"type":"enabled"},"stream":true}],"stream":false,"type":"response.create","model":"gpt-5.4"}`,
		`{"stream":null,"type":null,"thinking":null,"reasoning":null}`,
		`{"stream":"yes","type":42,"thinking":{"type":false},"reasoning":{"effort":[]}}`,
		`{"stream":false,"stream":true,"reasoning":{"effort":"low"},"reasoning":{"effort":"high"}}`,
		`{"thinking":{},"thinking":{"type":"enabled"},"reasoning":{},"reasoning":{"effort":"high"}}`,
		`{"thinking":{"type":null,"type":"enabled"},"reasoning":{"effort":null,"effort":"high"}}`,
		`{"STREAM":true,"str\u0065am":false,"th\u0069nking":{"type":" ADAPTIVE "},"reasoning_effort":"low"}`,
		`{"reasoning":{"effort":""},"reasoning_effort":"high","thinking":{"type":"disabled"},"model":"glm-5.3"}`,
		`{"reasoning":{"effort":"unknown"},"thinking":{"type":" Enabled "},"model":"gpt-5.4"}`,
		`{"model":"glm-5.3","reasoning":{"effort":"max"},"instructions":"quote: \"model\" newline: \n unicode: 中文"}`,
		`{"model":"gpt-5.4","reasoning":{"effort":"max"},"thinking":{"type":"enabled"}}`,
		`[]`, `null`, `"stream"`,
	} {
		t.Run(body, func(t *testing.T) { assertRoutingMetadataEquivalent(t, []byte(body)) })
	}
}

func FuzzGatewayRoutingMetadataPreservesSelectors(f *testing.F) {
	f.Add([]byte(`{"input":[{"role":"user","content":"hi"}],"stream":false,"reasoning":{"effort":"high"}}`))
	f.Add([]byte(`{"stream":null,"stream":true,"thinking":{},"thinking":{"type":"enabled"}}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if !json.Valid(body) {
			t.Skip()
		}
		assertRoutingMetadataEquivalent(t, body)
	})
}

func TestWithRequestProfileRoutingMetadataKeepsCanonicalBody(t *testing.T) {
	r := NewUniversalRoutingResolver(&stubSpanLister{})
	r.router = NewProtocolRouter()
	for _, tc := range []struct {
		name, fields     string
		stream, thinking bool
	}{
		{"effort high", `"stream":true,"reasoning":{"effort":"high"}`, true, true},
		{"effort low", `"stream":false,"reasoning":{"effort":"low"}`, false, false},
		{"native thinking wins", `"stream":null,"thinking":{"type":"enabled"},"reasoning":{"effort":"low"}`, false, true},
		{"normalized fallback", `"thinking":{"type":" ADAPTIVE "}`, false, true},
		{"websocket create", `"type":"response.create"`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"payload preserved"}],` + tc.fields + `}`)
			profile, ok := candidateRequestProfile(ShapeOpenAIChat, "/v1/responses", "gpt-5.4", body)
			require.True(t, ok)
			ctx := r.WithRequestProfile(context.Background(), ShapeOpenAIChat, "/v1/responses", "gpt-5.4", body, profile)
			request, ok := ProtocolRoutingRequest(ctx)
			require.True(t, ok)
			require.Equal(t, tc.stream, request.Profile().Stream)
			thinking, ok := ThinkingEnabledFromContext(ctx)
			require.True(t, ok)
			require.Equal(t, tc.thinking, thinking)
			require.Equal(t, body, request.Body())
			profile.Stream = tc.stream
			original, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
				InboundProtocol: protocolrouter.ProtocolResponses, RequestedModel: "gpt-5.4", ResponsesPath: protocolrouter.ResponsesPathRoot, Profile: profile, Body: body,
			})
			require.NoError(t, err)
			require.Equal(t, original.Digest(), request.Digest())
		})
	}
	for _, field := range []string{`"stream":"yes"`, `"type":true`} {
		body := []byte(`{"model":"gpt-5.4","input":"hi",` + field + `}`)
		ctx := r.WithRequestProfile(context.Background(), ShapeOpenAIChat, "/v1/responses", "gpt-5.4", body, protocolrouter.RequestProfile{})
		_, ok := ProtocolRoutingRequest(ctx)
		require.False(t, ok, "mistyped routing metadata must still fail closed")
	}
}

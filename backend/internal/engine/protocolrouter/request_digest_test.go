package protocolrouter

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
)

func TestCanonicalDigestPreservesEncoding(t *testing.T) {
	profiles := []RequestProfile{{}, {Stream: true, Tools: true, ToolChoice: ToolChoiceNamed,
		Continuation: ContinuationPreviousResponse, Reasoning: ReasoningSummary,
		PromptCache: PromptCachePlacement, ContentKinds: ContentText | ContentImage | ContentUnknown}}
	for _, protocol := range AllProtocols() {
		for _, profile := range profiles {
			for _, body := range [][]byte{[]byte(`{"input":"hi"}`), bytes.Repeat([]byte("\x00你好"), 32768)} {
				request, err := NewCanonicalRequest(CanonicalRequestInput{InboundProtocol: protocol,
					ResponsesPath: ResponsesPathCompact, RequestedModel: "model\x00名称", Profile: profile, Body: body})
				if err != nil {
					t.Fatal(err)
				}
				// Independent reference for the existing length-prefixed encoding.
				var encoded bytes.Buffer
				for _, value := range []string{string(protocol), request.RequestedModel(), string(request.ResponsesPath())} {
					_ = binary.Write(&encoded, binary.BigEndian, uint64(len(value)))
					_, _ = encoded.WriteString(value)
				}
				_ = binary.Write(&encoded, binary.BigEndian, profile.Stream)
				_ = binary.Write(&encoded, binary.BigEndian, profile.Tools)
				for _, value := range []string{string(profile.ToolChoice), string(profile.Continuation), string(profile.Reasoning), string(profile.PromptCache)} {
					_ = binary.Write(&encoded, binary.BigEndian, uint64(len(value)))
					_, _ = encoded.WriteString(value)
				}
				_ = binary.Write(&encoded, binary.BigEndian, uint32(profile.ContentKinds))
				_ = binary.Write(&encoded, binary.BigEndian, uint64(len(body)))
				_, _ = encoded.Write(body)
				if want := RequestDigest(sha256.Sum256(encoded.Bytes())); request.Digest() != want {
					t.Fatalf("%s profile %+v: digest differs from existing encoding", protocol, profile)
				}
			}
		}
	}
}

func BenchmarkNewCanonicalRequest(b *testing.B) {
	for _, size := range []int{4 << 10, 128 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			input := CanonicalRequestInput{InboundProtocol: ProtocolResponses,
				RequestedModel: "example-model", Body: make([]byte, size)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := NewCanonicalRequest(input); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

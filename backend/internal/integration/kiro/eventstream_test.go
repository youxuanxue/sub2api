//go:build unit

package kiro

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// buildEventStreamMessage hand-assembles a single AWS Event Stream binary frame
// with one String header `:event-type` and a JSON payload, matching the byte
// layout decoded by parseEventStream / extractEventType in client.go.
//
// Frame layout:
//
//	prelude (12B): totalLen(4) | headersLen(4) | preludeCRC(4, unchecked)
//	headers:       [ nameLen(1) | name | valueType(1)=7 | valueLen(2) | value ]...
//	payload:       JSON bytes
//	messageCRC:    4B (unchecked by parser, filled with zeros)
func buildEventStreamMessage(eventType string, payload []byte) []byte {
	// Build the headers block: a single String-typed `:event-type` header.
	const headerName = ":event-type"
	var headers bytes.Buffer
	headers.WriteByte(byte(len(headerName))) // name length (1B)
	headers.WriteString(headerName)          // header name
	headers.WriteByte(7)                     // value type 7 = String
	var vlen [2]byte
	binary.BigEndian.PutUint16(vlen[:], uint16(len(eventType)))
	headers.Write(vlen[:])         // value length (2B BE)
	headers.WriteString(eventType) // value

	headerBytes := headers.Bytes()
	headersLen := len(headerBytes)
	// total = prelude(12) + headers + payload + messageCRC(4)
	totalLen := 12 + headersLen + len(payload) + 4

	var frame bytes.Buffer
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], uint32(totalLen))
	frame.Write(u32[:]) // total length
	binary.BigEndian.PutUint32(u32[:], uint32(headersLen))
	frame.Write(u32[:])             // headers length
	frame.Write([]byte{0, 0, 0, 0}) // prelude CRC (unchecked)
	frame.Write(headerBytes)        // headers
	frame.Write(payload)            // payload
	frame.Write([]byte{0, 0, 0, 0}) // message CRC (unchecked)

	return frame.Bytes()
}

// TestParseEventStream_AssistantResponse feeds a hand-built assistantResponseEvent
// frame to parseEventStream and asserts the OnText callback receives the payload
// content.
func TestParseEventStream_AssistantResponse(t *testing.T) {
	frame := buildEventStreamMessage("assistantResponseEvent", []byte(`{"content":"hi"}`))
	frame = append(frame, buildEventStreamMessage("metadataEvent", []byte(`{"stopReason":"END_TURN"}`))...)

	var gotText string
	var gotThinking bool
	cb := &KiroStreamCallback{
		OnText: func(text string, isThinking bool) {
			gotText += text
			gotThinking = gotThinking || isThinking
		},
	}

	if err := parseEventStream(bytes.NewReader(frame), cb); err != nil {
		t.Fatalf("parseEventStream returned error: %v", err)
	}

	if gotText != "hi" {
		t.Fatalf("expected OnText to receive %q, got %q", "hi", gotText)
	}
	if gotThinking {
		t.Fatalf("expected non-thinking text for assistantResponseEvent, got isThinking=true")
	}
}

func TestParseEventStream_FrameAlignedEOFWithoutStopReasonFails(t *testing.T) {
	frame := buildEventStreamMessage("assistantResponseEvent", []byte(`{"content":"partial"}`))

	err := parseEventStream(bytes.NewReader(frame), &KiroStreamCallback{})
	if !errors.Is(err, ErrIncompleteEventStream) {
		t.Fatalf("expected ErrIncompleteEventStream, got %v", err)
	}
}

func TestParseEventStream_TerminalStopIgnoresTruncatedTrailingFrame(t *testing.T) {
	stream := buildEventStreamMessage("assistantResponseEvent", []byte(`{"content":"complete"}`))
	stream = append(stream, buildEventStreamMessage("metadataEvent", []byte(`{"stopReason":"END_TURN"}`))...)
	stream = append(stream, []byte{0, 0, 0, 20}...)

	if err := parseEventStream(bytes.NewReader(stream), &KiroStreamCallback{}); err != nil {
		t.Fatalf("terminal stream must remain successful after a truncated trailing frame: %v", err)
	}
}

func TestParseEventStream_MetadataStopReason(t *testing.T) {
	frame := buildEventStreamMessage("metadataEvent", []byte(`{"stopReason":"CONTENT_FILTERED"}`))

	var got string
	cb := &KiroStreamCallback{
		OnStopReason: func(stopReason string) {
			got = stopReason
		},
	}

	if err := parseEventStream(bytes.NewReader(frame), cb); err != nil {
		t.Fatalf("parseEventStream returned error: %v", err)
	}
	if got != "CONTENT_FILTERED" {
		t.Fatalf("expected stop reason %q, got %q", "CONTENT_FILTERED", got)
	}
}

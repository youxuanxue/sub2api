//go:build unit

package kiro

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKiroReliabilityFrameValidation(t *testing.T) {
	stop := buildEventStreamMessage("metadataEvent", []byte(`{"stopReason":"END_TURN"}`))
	text := buildEventStreamMessage("assistantResponseEvent", []byte(`{"content":"ok"}`))
	badPrelude := append([]byte(nil), text...)
	badPrelude[8] ^= 1
	badCRC := append([]byte(nil), text...)
	badCRC[len(badCRC)-1] ^= 1
	invalidUTF := buildEventStreamMessage("assistantResponseEvent", append(append([]byte(`{"content":"`), 0xff), []byte(`"}`)...))
	huge := append([]byte(nil), text[:12]...)
	binary.BigEndian.PutUint32(huge[:4], 0xffffffff)
	short := append([]byte(nil), text[:12]...)
	binary.BigEndian.PutUint32(short[:4], 15)
	badHeaders := append([]byte(nil), text...)
	badHeaders[12] = 255
	binary.BigEndian.PutUint32(badHeaders[len(badHeaders)-4:], crc32.ChecksumIEEE(badHeaders[:len(badHeaders)-4]))
	for name, raw := range map[string][]byte{
		"prelude CRC": badPrelude, "message CRC": badCRC, "UTF8": invalidUTF, "huge length": huge, "short length": short, "header overrun": badHeaders,
		"bad JSON":       buildEventStreamMessage("assistantResponseEvent", []byte(`{"content":`)),
		"null JSON":      buildEventStreamMessage("assistantResponseEvent", []byte(`null`)),
		"late text":      append(append([]byte(nil), stop...), text...),
		"late reasoning": append(append([]byte(nil), stop...), buildEventStreamMessage("reasoningContentEvent", []byte(`{"text":"late"}`))...),
		"duplicate stop": append(append([]byte(nil), stop...), stop...),
	} {
		t.Run(name, func(t *testing.T) {
			emitted, completed := false, false
			raw = append(append([]byte(nil), raw...), stop...)
			err := parseEventStream(bytes.NewReader(raw), &KiroStreamCallback{OnText: func(string, bool) { emitted = true }, OnComplete: func(int, int) { completed = true }})
			require.Error(t, err)
			require.False(t, completed)
			require.False(t, emitted)
		})
	}
	for _, kind := range []string{"meteringEvent", "contextUsageEvent"} {
		t.Run(kind, func(t *testing.T) {
			credits, pct := float64(0), float64(0)
			raw := append(append([]byte(nil), stop...), buildEventStreamMessage(kind, []byte(`{"usage":2.5,"contextUsagePercentage":10}`))...)
			require.NoError(t, parseEventStream(bytes.NewReader(raw), &KiroStreamCallback{OnCredits: func(v float64) { credits = v }, OnContextUsage: func(v float64) { pct = v }}))
			if kind == "meteringEvent" {
				require.Equal(t, 2.5, credits)
			} else {
				require.Equal(t, float64(10), pct)
			}
		})
	}
}

func TestKiroReliabilityLateToolID(t *testing.T) {
	for _, events := range [][]string{
		{`{"name":"read","input":"{\"path\":"}`, `{"toolUseId":"late","input":"\"中文.txt\"}","stop":true}`},
		{`{"name":"read","input":{"path":"中文.txt"}}`, `{"toolUseId":"late","stop":true}`},
	} {
		var got []KiroToolUse
		require.NoError(t, parseEventStream(bytes.NewReader(toolEventStream(events...)), &KiroStreamCallback{OnToolUse: func(v KiroToolUse) { got = append(got, v) }}))
		require.Equal(t, []KiroToolUse{{ToolUseID: "late", Name: "read", Input: map[string]any{"path": "中文.txt"}}}, got)
	}
	for _, events := range [][]string{
		{`{"name":"read","input":"{"}`, `{"name":"write","input":"{"}`, `{"toolUseId":"late","input":"}","stop":true}`},
		{`{"name":"read","input":{}}`, `{"stop":true}`},
		{`{"name":"read","input":"{"}`, `{"name":"read","input":"}"}`, `{"toolUseId":"late","stop":true}`},
		{`{"name":"read","input":"{"}`, `{"toolUseId":"late","name":"write","input":"}","stop":true}`},
	} {
		emitted := false
		err := parseEventStream(bytes.NewReader(toolEventStream(events...)), &KiroStreamCallback{OnToolUse: func(KiroToolUse) { emitted = true }})
		require.ErrorIs(t, err, ErrInvalidToolUse)
		require.False(t, emitted)
	}
}

func TestKiroReliabilityUnicodeEscapes(t *testing.T) {
	for _, raw := range []string{`{"text":"\ud800"}`, `{"text":"\udc00"}`, `{"text":"\ud800\u0041"}`} {
		require.False(t, validKiroJSON([]byte(raw)))
	}
	for _, raw := range []string{`{"text":"\ud83d\ude42"}`, `{"text":"🙂�"}`, `{"text":"\\ud800"}`} {
		require.True(t, validKiroJSON([]byte(raw)))
	}
	emitted := false
	stream := toolEventStream(`{"name":"read","toolUseId":"a","input":"{\"path\":\"\\ud800\"}","stop":true}`)
	err := parseEventStream(bytes.NewReader(stream), &KiroStreamCallback{OnToolUse: func(KiroToolUse) { emitted = true }})
	require.ErrorIs(t, err, ErrInvalidToolUse)
	require.False(t, emitted)
}

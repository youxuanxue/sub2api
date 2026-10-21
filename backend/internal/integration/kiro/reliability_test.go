//go:build unit

package kiro

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"net/http"
	"strings"
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

func TestKiroReliabilityMeteringEventCompletesWithoutMetadata(t *testing.T) {
	stream := buildEventStreamMessage("assistantResponseEvent", []byte(`{"content":"complete"}`))
	stream = append(stream, buildEventStreamMessage("contextUsageEvent", []byte(`{"contextUsagePercentage":12}`))...)
	stream = append(stream, buildEventStreamMessage("meteringEvent", []byte(`{"usage":2.5}`))...)
	var text, stop string
	err := parseEventStream(bytes.NewReader(stream), &KiroStreamCallback{
		OnText:       func(s string, _ bool) { text += s },
		OnStopReason: func(s string) { stop = s },
	})
	require.NoError(t, err)
	require.Equal(t, "complete", text)
	require.Equal(t, "END_TURN", stop)
}

func TestKiroReliabilityMeteringOnlyDoesNotComplete(t *testing.T) {
	stream := buildEventStreamMessage("contextUsageEvent", []byte(`{"contextUsagePercentage":12}`))
	stream = append(stream, buildEventStreamMessage("meteringEvent", []byte(`{"usage":2.5}`))...)
	require.ErrorIs(t, parseEventStream(bytes.NewReader(stream), &KiroStreamCallback{}), ErrIncompleteEventStream)
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

func TestKiroReliabilityProfileRecoveryCannotReplayCommittedOutput(t *testing.T) {
	for _, resetProvided := range []bool{true, false} {
		t.Run(map[bool]string{true: "consumer_refuses_reset", false: "no_reset_contract"}[resetProvided], func(t *testing.T) {
			calls, discoveries := 0, 0
			text := ""
			doer := restDoerFunc(func(req *http.Request) (*http.Response, error) {
				if strings.Contains(req.URL.Path, "ListAvailableProfiles") {
					discoveries++
					return restTestResponse(200, `{"profiles":[{"arn":"arn:aws:codewhisperer:us-east-1:1:profile/fresh"}]}`), nil
				}
				calls++
				raw := buildEventStreamMessage("assistantResponseEvent", []byte(`{"content":"already delivered"}`))
				if calls == 1 {
					raw = append(raw, buildEventStreamMessage("error", []byte(`{"message":"Invalid profileArn."}`))...)
				} else {
					raw = append(raw, terminalEventStreamForTest("END_TURN")...)
				}
				return restTestResponse(200, string(raw)), nil
			})
			callback := &KiroStreamCallback{OnText: func(v string, _ bool) { text += v }}
			if resetProvided {
				callback.ResetForRetry = func() bool { return false }
			}
			account := &Account{AccessToken: "fixture", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/stale"}
			err := CallKiroAPIWithDoerContext(context.Background(), doer, account, &KiroPayload{}, callback)
			require.Error(t, err)
			require.Equal(t, 1, calls)
			require.Zero(t, discoveries)
			require.Equal(t, "already delivered", text)
		})
	}
}

func TestKiroReliabilityRejectsMalformedContentFields(t *testing.T) {
	for _, tc := range []struct{ event, payload string }{
		{"assistantResponseEvent", `{"content":123}`},
		{"reasoningContentEvent", `{"text":[]}`},
		{"reasoningContentEvent", `{"signature":{}}`},
		{"metadataEvent", `{"stopReason":123}`},
	} {
		t.Run(tc.event+tc.payload, func(t *testing.T) {
			raw := append(buildEventStreamMessage("assistantResponseEvent", []byte(`{"content":"prefix"}`)), buildEventStreamMessage(tc.event, []byte(tc.payload))...)
			raw = append(raw, terminalEventStreamForTest("END_TURN")...)
			completed := false
			err := parseEventStream(bytes.NewReader(raw), &KiroStreamCallback{OnComplete: func(int, int) { completed = true }})
			require.ErrorIs(t, err, ErrInvalidEventStream)
			require.False(t, completed)
		})
	}
}

func TestKiroReliabilityOptionalNullTextFields(t *testing.T) {
	raw := buildEventStreamMessage("assistantResponseEvent", []byte(`{"content":null}`))
	raw = append(raw, buildEventStreamMessage("reasoningContentEvent", []byte(`{"text":null,"signature":null}`))...)
	raw = append(raw, buildEventStreamMessage("metadataEvent", []byte(`{"stopReason":null}`))...)
	raw = append(raw, buildEventStreamMessage("assistantResponseEvent", []byte(`{"content":"complete"}`))...)
	raw = append(raw, terminalEventStreamForTest("END_TURN")...)
	text := ""
	completed := false
	err := parseEventStream(bytes.NewReader(raw), &KiroStreamCallback{OnText: func(v string, _ bool) { text += v }, OnComplete: func(int, int) { completed = true }})
	require.NoError(t, err)
	require.True(t, completed)
	require.Equal(t, "complete", text)
}

//go:build unit

package kiro

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func toolEventStream(events ...string) []byte {
	var stream []byte
	for _, event := range events {
		stream = append(stream, buildEventStreamMessage("toolUseEvent", []byte(event))...)
	}
	return append(stream, buildEventStreamMessage("metadataEvent", []byte(`{"stopReason":"TOOL_USE"}`))...)
}

func TestParseEventStream_RejectsInvalidToolEvents(t *testing.T) {
	cases := []struct {
		name   string
		events []string
	}{
		{"broken event JSON", []string{`{"toolUseId":`}},
		{"broken input JSON", []string{`{"toolUseId":"a","name":"read","input":"{","stop":true}`}},
		{"missing input", []string{`{"toolUseId":"a","name":"read","stop":true}`}},
		{"null input", []string{`{"toolUseId":"a","name":"read","input":"null","stop":true}`}},
		{"array input", []string{`{"toolUseId":"a","name":"read","input":"[]","stop":true}`}},
		{"multiple JSON values", []string{`{"toolUseId":"a","name":"read","input":"{} {}","stop":true}`}},
		{"unsupported input type", []string{`{"toolUseId":"a","name":"read","input":42,"stop":true}`}},
		{"missing stop", []string{`{"toolUseId":"a","name":"read","input":"{}"}`}},
		{"false stop", []string{`{"toolUseId":"a","name":"read","input":"{}","stop":false}`}},
		{"invalid stop type", []string{`{"toolUseId":"a","name":"read","input":"{}","stop":"true"}`}},
		{"missing id", []string{`{"name":"read","input":"{}","stop":true}`}},
		{"missing name", []string{`{"toolUseId":"a","input":"{}","stop":true}`}},
		{"invalid name type", []string{`{"toolUseId":"a","name":42,"input":"{}","stop":true}`}},
		{"name changes mid call", []string{`{"toolUseId":"a","name":"read","input":"{"}`, `{"toolUseId":"a","name":"write","input":"}","stop":true}`}},
		{"object overwrites partial input", []string{`{"toolUseId":"a","name":"read","input":"{"}`, `{"toolUseId":"a","input":{},"stop":true}`}},
		{"ambiguous continuation", []string{`{"toolUseId":"a","name":"read","input":"{"}`, `{"toolUseId":"b","name":"read","input":"{"}`, `{"input":"}","stop":true}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tools []KiroToolUse
			completed, stopped := false, false
			err := parseEventStream(bytes.NewReader(toolEventStream(tc.events...)), &KiroStreamCallback{
				OnToolUse:    func(tool KiroToolUse) { tools = append(tools, tool) },
				OnComplete:   func(int, int) { completed = true },
				OnStopReason: func(string) { stopped = true },
			})
			require.ErrorIs(t, err, ErrInvalidToolUse)
			require.Empty(t, tools, "an invalid tool must never become an executable callback")
			require.False(t, completed)
			require.False(t, stopped)
		})
	}
}

func TestParseEventStream_CompleteToolEvents(t *testing.T) {
	cases := []struct {
		name   string
		events []string
		want   []KiroToolUse
	}{
		{"explicit empty object", []string{`{"toolUseId":"a","name":"ping","input":{},"stop":true}`}, []KiroToolUse{{ToolUseID: "a", Name: "ping", Input: map[string]any{}}}},
		{"fragmented input and omitted continuation identity", []string{`{"toolUseId":"a","name":"read","input":"{\"path\":"}`, `{"input":"\"fixture\"}"}`, `{"stop":true}`}, []KiroToolUse{{ToolUseID: "a", Name: "read", Input: map[string]any{"path": "fixture"}}}},
		{"sequential tools", []string{`{"toolUseId":"a","name":"ping","input":"{}","stop":true}`, `{"toolUseId":"b","name":"ping","input":"{}","stop":true}`}, []KiroToolUse{{ToolUseID: "a", Name: "ping", Input: map[string]any{}}, {ToolUseID: "b", Name: "ping", Input: map[string]any{}}}},
		{"interleaved tools with explicit ids", []string{`{"toolUseId":"a","name":"read","input":"{\"path\":"}`, `{"toolUseId":"b","name":"read","input":"{\"path\":"}`, `{"toolUseId":"b","input":"\"second\"}","stop":true}`, `{"toolUseId":"a","input":"\"first\"}","stop":true}`}, []KiroToolUse{{ToolUseID: "b", Name: "read", Input: map[string]any{"path": "second"}}, {ToolUseID: "a", Name: "read", Input: map[string]any{"path": "first"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tools []KiroToolUse
			completed := false
			err := parseEventStream(bytes.NewReader(toolEventStream(tc.events...)), &KiroStreamCallback{
				OnToolUse:  func(tool KiroToolUse) { tools = append(tools, tool) },
				OnComplete: func(int, int) { completed = true },
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, tools)
			require.True(t, completed)
		})
	}
}

func TestParseEventStream_DuplicateToolIDDoesNotExecuteTwice(t *testing.T) {
	event := `{"toolUseId":"a","name":"ping","input":"{}","stop":true}`
	var tools []KiroToolUse
	err := parseEventStream(bytes.NewReader(toolEventStream(event, event)), &KiroStreamCallback{
		OnToolUse: func(tool KiroToolUse) { tools = append(tools, tool) },
	})
	require.Error(t, err)
	require.Equal(t, []KiroToolUse{{ToolUseID: "a", Name: "ping", Input: map[string]any{}}}, tools)
}

func TestParseEventStream_InterruptedToolIsNotEmitted(t *testing.T) {
	partial := buildEventStreamMessage("toolUseEvent", []byte(`{"toolUseId":"a","name":"read","input":"{\"path\":\"fixture\"}"}`))
	for _, tail := range [][]byte{nil, {0, 0, 0, 20}} {
		stream := append(append([]byte(nil), partial...), tail...)
		var tools []KiroToolUse
		err := parseEventStream(bytes.NewReader(stream), &KiroStreamCallback{OnToolUse: func(tool KiroToolUse) { tools = append(tools, tool) }})
		require.Error(t, err)
		require.Empty(t, tools)
	}
}

func TestParseEventStream_ToolInputNumbersRemainExact(t *testing.T) {
	for _, event := range []string{
		`{"toolUseId":"a","name":"read","input":"{\"revision\":9007199254740993}","stop":true}`,
		`{"toolUseId":"a","name":"read","input":{"revision":9007199254740993},"stop":true}`,
	} {
		var input map[string]any
		err := parseEventStream(bytes.NewReader(toolEventStream(event)), &KiroStreamCallback{OnToolUse: func(tool KiroToolUse) { input = tool.Input }})
		require.NoError(t, err)
		encoded, err := json.Marshal(input)
		require.NoError(t, err)
		require.Equal(t, `{"revision":9007199254740993}`, string(encoded))
	}
}

func TestParseEventStream_RejectsToolAfterTerminalStop(t *testing.T) {
	stream := buildEventStreamMessage("metadataEvent", []byte(`{"stopReason":"END_TURN"}`))
	stream = append(stream, buildEventStreamMessage("toolUseEvent", []byte(`{"toolUseId":"a","name":"ping","input":{},"stop":true}`))...)
	var tools []KiroToolUse
	completed := false
	err := parseEventStream(bytes.NewReader(stream), &KiroStreamCallback{
		OnToolUse:  func(tool KiroToolUse) { tools = append(tools, tool) },
		OnComplete: func(int, int) { completed = true },
	})
	require.ErrorIs(t, err, ErrInvalidToolUse)
	require.Empty(t, tools)
	require.False(t, completed)
}

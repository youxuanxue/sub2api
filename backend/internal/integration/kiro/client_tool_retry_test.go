//go:build unit

package kiro

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type toolStreamDoer struct {
	calls  int
	stream []byte
}

func (d *toolStreamDoer) Do(*http.Request) (*http.Response, error) {
	d.calls++
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(d.stream))}, nil
}

func TestCallKiroAPI_ToolInputOmissionAndInvalidInputDoNotAmplifyEndpoints(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "schema-valid omission", false: "malformed JSON"}[valid], func(t *testing.T) {
			event := `{"name":"probe_empty","toolUseId":"a","stop":true}`
			if !valid {
				event = `{"name":"probe_empty","toolUseId":"a","input":"{","stop":true}`
			}
			doer := &toolStreamDoer{stream: toolEventStream(event)}
			payload := &KiroPayload{}
			var tool KiroToolWrapper
			tool.ToolSpecification.Name = "probe_empty"
			tool.ToolSpecification.InputSchema.JSON = map[string]any{"type": "object", "properties": map[string]any{}}
			payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext = &UserInputMessageContext{Tools: []KiroToolWrapper{tool}}
			resets, emitted := 0, 0
			err := CallKiroAPIWithDoerContext(context.Background(), doer, &Account{AccessToken: "fixture", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/x"}, payload, &KiroStreamCallback{
				ResetForRetry: func() bool { resets++; return true }, OnToolUse: func(tool KiroToolUse) { emitted++; require.Equal(t, map[string]any{}, tool.Input) },
			})
			if valid {
				require.NoError(t, err)
				require.Equal(t, 1, emitted)
			} else {
				require.ErrorIs(t, err, ErrInvalidToolUse)
				require.Zero(t, emitted)
			}
			require.Equal(t, 1, doer.calls)
			require.Zero(t, resets)
		})
	}
}

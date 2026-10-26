package kiro

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPayloadSizeDiagnosticsPreservesProtectedText(t *testing.T) {
	const secret = "private transcript <>&\n"
	p := &KiroPayload{}
	// Both real translators normalize before calling the truncator.
	p.ConversationState.CurrentMessage.UserInputMessage.Content = strings.TrimSpace(strings.Repeat(secret, 60000))
	before, err := json.Marshal(p)
	require.NoError(t, err)
	truncatePayloadToLimit(p, false)
	after, err := json.Marshal(p)
	require.NoError(t, err)
	require.True(t, string(before) == string(after), "diagnostics must not change protected text or wire shape")
	d := p.SizeDiagnostics
	require.Equal(t, len(before), d.Before.TotalBytes)
	require.Equal(t, d.Before, d.After)
	textJSON, err := json.Marshal(p.ConversationState.CurrentMessage.UserInputMessage.Content)
	require.NoError(t, err)
	require.Equal(t, len(textJSON), d.After.CurrentTextBytes, "count JSON escaping, not character count")
	require.Equal(t, d.After.TotalBytes, d.After.CurrentTextBytes+d.After.OtherBytes)
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private transcript")
	require.Error(t, ValidatePayloadSize(p))
}

func TestPayloadSizeDiagnosticsTracksHistoryReduction(t *testing.T) {
	p := &KiroPayload{}
	p.ConversationState.CurrentMessage.UserInputMessage.Content = "current"
	p.ConversationState.History = []KiroHistoryMessage{
		{UserInputMessage: &KiroUserInputMessage{Content: "system"}},
		{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: "ack"}},
		{UserInputMessage: &KiroUserInputMessage{Content: strings.Repeat("old", 400000)}},
		{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: "reply"}},
	}
	truncatePayloadToLimit(p, true)
	d := p.SizeDiagnostics
	require.Greater(t, d.Before.TotalBytes, d.LimitBytes)
	require.LessOrEqual(t, d.After.TotalBytes, d.LimitBytes)
	require.Equal(t, d.Before.SystemPrimingBytes, d.After.SystemPrimingBytes)
	require.Less(t, d.After.HistoryBytes, d.Before.HistoryBytes)
	require.Equal(t, "current", p.ConversationState.CurrentMessage.UserInputMessage.Content)
	require.NoError(t, ValidatePayloadSize(p))
	require.Equal(t, payloadByteSize(p), d.After.TotalBytes)
	require.Equal(t, d.After.TotalBytes, d.After.SystemPrimingBytes+d.After.HistoryBytes+d.After.CurrentTextBytes+d.After.OtherBytes)
}

func TestSmallPayloadHasNoDiagnosticsOrWireChange(t *testing.T) {
	p := &KiroPayload{}
	p.ConversationState.CurrentMessage.UserInputMessage.Content = "small"
	before, err := json.Marshal(p)
	require.NoError(t, err)
	truncatePayloadToLimit(p, false)
	after, err := json.Marshal(p)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Nil(t, p.SizeDiagnostics)
}

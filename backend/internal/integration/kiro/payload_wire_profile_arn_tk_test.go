//go:build unit

package kiro

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Length matches every prod Kiro account's profile_arn (65 characters), so the
// 81-byte wire cost these tests assert is the real one.
const wireTestProfileArn = "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRRHZ9W"

func wireTestAccount() *Account {
	return &Account{ProfileArn: wireTestProfileArn}
}

// proseOnlyPayload builds a payload whose only trimmable surface is current
// user prose, which the approved policy forbids shortening. Reduction
// therefore cannot save it, so size behavior is visible without truncation
// interfering.
func proseOnlyPayload(prose int) *KiroPayload {
	p := &KiroPayload{}
	p.ConversationState.ChatTriggerType = "MANUAL"
	p.ConversationState.ConversationID = "wire-fixture"
	p.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: strings.Repeat("x", prose),
		ModelID: "claude-sonnet-5",
		Origin:  "AI_EDITOR",
	}
	return p
}

// proseFillForTotal returns the prose length that makes the serialized payload
// exactly total bytes, derived by measuring the envelope rather than hardcoding it.
func proseFillForTotal(t *testing.T, total int) int {
	t.Helper()
	envelope := payloadByteSize(proseOnlyPayload(0))
	fill := total - envelope
	require.Positive(t, fill, "envelope %d already exceeds target %d", envelope, total)
	require.Equal(t, total, payloadByteSize(proseOnlyPayload(fill)))
	return fill
}

// The gap the defect hid: profileArn is omitempty, so an unset field costs
// nothing and the entire member lands on the wire unmeasured.
func TestWireProfileArnCostsEightyOneBytes(t *testing.T) {
	p := proseOnlyPayload(64)
	atValidate := payloadByteSize(p)

	setPayloadProfileArnForAccount(p, wireTestAccount())
	onWire := payloadByteSize(p)

	require.Equal(t, 81, onWire-atValidate,
		"65-char ARN plus the 16-byte `,\"profileArn\":\"\"` shell")
	require.Len(t, wireTestProfileArn, 65)

	// Nothing of the member is serialized while the field is empty.
	require.NotContains(t, string(mustMarshal(t, proseOnlyPayload(64))), "profileArn")
}

// Direction 1 — the injected regression. A payload that the guard measured as
// fitting went on the wire over the limit. Without BindWireProfileArn the guard
// accepts it; with it the request is rejected.
func TestWireGuardRejectsPayloadOnlyTheArnPushesOver(t *testing.T) {
	// Land inside the 81-byte window: fits bare, overflows once the ARN is on.
	fill := proseFillForTotal(t, maxPayloadBytes-40)
	p := proseOnlyPayload(fill)

	// The bug: the old measurement passes.
	require.LessOrEqual(t, payloadByteSize(p), maxPayloadBytes)
	require.NoError(t, ValidatePayloadSize(p),
		"fixture must pass the pre-fix guard, else it proves nothing")

	// The wire body it would have produced is over the limit.
	wire := proseOnlyPayload(fill)
	setPayloadProfileArnForAccount(wire, wireTestAccount())
	wireSize := payloadByteSize(wire)
	require.Greater(t, wireSize, maxPayloadBytes,
		"fixture must exceed the limit once the ARN is injected")

	// The fix: bind first, and the same request is now correctly rejected.
	BindWireProfileArn(p, wireTestAccount())
	var tooLarge *PayloadTooLargeError
	require.ErrorAs(t, ValidatePayloadSize(p), &tooLarge)
	require.Equal(t, maxPayloadBytes, tooLarge.Limit)
	require.Equal(t, wireSize, tooLarge.Size, "the 413 must report the wire size")
	t.Logf("at validate=%d on wire=%d gap=%+d", maxPayloadBytes-40, wireSize, wireSize-(maxPayloadBytes-40))
}

// Binding before reduction, not just before validation, is what keeps the
// truncator and the guard on one basis. A payload that goes over only because
// of the ARN and still has droppable history must be reduced, not 413'd.
func TestWireBindReducesRatherThanRejectsWhenHistoryCanGo(t *testing.T) {
	p := proseOnlyPayload(4096)
	const turn = "an earlier exchange that may be dropped to make room"
	for i := 0; i < 6; i++ {
		p.ConversationState.History = append(p.ConversationState.History,
			KiroHistoryMessage{UserInputMessage: &KiroUserInputMessage{
				Content: turn, ModelID: "claude-sonnet-5", Origin: "AI_EDITOR",
			}},
			KiroHistoryMessage{AssistantResponseMessage: &KiroAssistantResponseMessage{
				Content: "understood",
			}},
		)
	}
	// Pad history so the payload sits just inside the 81-byte window.
	headroom := maxPayloadBytes - 40 - payloadByteSize(p)
	require.Positive(t, headroom)
	p.ConversationState.History[1].AssistantResponseMessage.Content = strings.Repeat("y", headroom)

	require.LessOrEqual(t, payloadByteSize(p), maxPayloadBytes)
	require.NoError(t, ValidatePayloadSize(p))
	prose := p.ConversationState.CurrentMessage.UserInputMessage.Content
	entriesBefore := len(p.ConversationState.History)

	BindWireProfileArn(p, wireTestAccount())

	require.NoError(t, ValidatePayloadSize(p),
		"a reducible payload must be trimmed, not rejected")
	require.LessOrEqual(t, payloadByteSize(p), maxPayloadBytes)
	require.Equal(t, wireTestProfileArn, p.ProfileArn)
	require.Less(t, len(p.ConversationState.History), entriesBefore,
		"reduction must have dropped history to make room")
	require.Equal(t, prose, p.ConversationState.CurrentMessage.UserInputMessage.Content,
		"current user prose must never be shortened")
}

// Direction 2 — no false rejection. A payload with real headroom is untouched
// apart from gaining the field it will send.
func TestWireBindLeavesFittingPayloadOtherwiseUnchanged(t *testing.T) {
	for _, fill := range []int{16, 4096, maxPayloadBytes / 2} {
		p := proseOnlyPayload(fill)
		before := mustMarshal(t, p)

		BindWireProfileArn(p, wireTestAccount())

		require.NoError(t, ValidatePayloadSize(p))
		require.Equal(t, wireTestProfileArn, p.ProfileArn)
		require.Nil(t, p.SizeDiagnostics, "no reduction should have run")

		// Byte-identical once the one field it must carry is removed again.
		stripped := *p
		stripped.ProfileArn = ""
		require.JSONEq(t, string(before), string(mustMarshal(t, &stripped)))
	}
}

// A payload already at the limit, ARN included, is still accepted: the fix must
// not shave usable capacity beyond the bytes actually sent.
func TestWireBindAcceptsPayloadExactlyAtLimitWithArn(t *testing.T) {
	fill := proseFillForTotal(t, maxPayloadBytes-81)
	p := proseOnlyPayload(fill)

	BindWireProfileArn(p, wireTestAccount())

	require.Equal(t, maxPayloadBytes, payloadByteSize(p))
	require.NoError(t, ValidatePayloadSize(p), "exactly at the limit must pass")
}

// An account without an ARN adds nothing, so no phantom 81 bytes.
func TestWireBindAddsNothingForAccountWithoutArn(t *testing.T) {
	fill := proseFillForTotal(t, maxPayloadBytes-40)

	for _, account := range []*Account{nil, {}, {ProfileArn: "   "}} {
		p := proseOnlyPayload(fill)
		before := mustMarshal(t, p)

		BindWireProfileArn(p, account)

		require.Equal(t, string(before), string(mustMarshal(t, p)),
			"no ARN to send means no bytes to account for")
		require.NoError(t, ValidatePayloadSize(p))
	}
}

// Repeated binding must not re-count the ARN or re-reduce the payload.
func TestWireBindIsIdempotent(t *testing.T) {
	p := proseOnlyPayload(4096)
	BindWireProfileArn(p, wireTestAccount())
	first := mustMarshal(t, p)
	firstEntries := len(p.ConversationState.History)

	for i := 0; i < 3; i++ {
		BindWireProfileArn(p, wireTestAccount())
	}

	require.Equal(t, string(first), string(mustMarshal(t, p)))
	require.Equal(t, firstEntries, len(p.ConversationState.History))
	require.Equal(t, 1, strings.Count(string(mustMarshal(t, p)), "profileArn"))
}

// Translation records a pre-truncation breakdown; a second reduction pass here
// must not overwrite it with a post-truncation snapshot.
func TestWireBindPreservesOriginalTruncationDiagnostics(t *testing.T) {
	req := &ClaudeRequest{
		Model:  "claude-sonnet-5",
		System: "system priming",
		Messages: []ClaudeMessage{
			{Role: "user", Content: strings.Repeat("a", 500*1024)},
			{Role: "assistant", Content: strings.Repeat("b", 500*1024)},
			{Role: "user", Content: "what now"},
		},
	}
	p := ClaudeToKiro(req, false)
	require.NotNil(t, p.SizeDiagnostics, "fixture must have truncated during translation")
	originalBefore := p.SizeDiagnostics.Before

	BindWireProfileArn(p, wireTestAccount())

	require.Equal(t, originalBefore, p.SizeDiagnostics.Before,
		"Before must stay the pre-truncation measurement")
	require.NoError(t, ValidatePayloadSize(p))
}

// The dispatch path still restores the caller's value, so binding upstream of
// it does not leave the account ARN behind on a shared payload.
func TestDispatchStillRestoresProfileArnAfterCall(t *testing.T) {
	p := proseOnlyPayload(64)
	p.ProfileArn = "arn:aws:codewhisperer:us-east-1:1:profile/caller-owned"
	original := p.ProfileArn

	err := CallKiroAPIWithDoer(&staleProfileChatDoer{}, wireTestAccount(), p, &KiroStreamCallback{})
	require.NoError(t, err)

	require.Equal(t, original, p.ProfileArn,
		"client.go must restore the pre-call value via defer")
}

func mustMarshal(t *testing.T, p *KiroPayload) []byte {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return raw
}

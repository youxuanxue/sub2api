package service

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkPassthroughPendingSSE_LimitZeroReleasesOnFirstClientOutput(t *testing.T) {
	p := newTkPassthroughPendingSSE(0)
	p.append(`data: {"type":"response.output_item.added"}`)
	require.True(t, p.shouldRelease(true, false), "limit<=0 must release immediately once client output starts")
	require.False(t, p.shouldRelease(false, false), "preamble/non-output lines must stay buffered")
}

func TestTkPassthroughPendingSSE_ForceReleaseIgnoresByteWindow(t *testing.T) {
	p := newTkPassthroughPendingSSE(4096)
	p.append("event: response.created")
	require.False(t, p.shouldRelease(true, false), "under limit without force must hold")
	require.True(t, p.shouldRelease(true, true), "forceRelease (terminal/failed) must flush held window")
}

func TestTkPassthroughPendingSSE_ReleasesWhenBufferedBytesReachLimit(t *testing.T) {
	p := newTkPassthroughPendingSSE(20)
	line := "data: " + strings.Repeat("x", 10) // len(line)+1 == 17
	p.append(line)
	require.False(t, p.shouldRelease(true, false))
	p.append("data: y") // +8 → 25 >= 20
	require.Equal(t, 17+8, p.bytes)
	require.True(t, p.shouldRelease(true, false))
}

func TestTkPassthroughPendingSSE_WriteAndClearDoesNotFlushAndResets(t *testing.T) {
	p := newTkPassthroughPendingSSE(64)
	p.append("data: one")
	p.append("data: two")
	var buf bytes.Buffer
	ok := p.writeAndClear(&buf, 42)
	require.True(t, ok)
	require.Equal(t, "data: one\ndata: two\n", buf.String())
	require.Zero(t, p.bytes)
	require.Empty(t, p.lines)
}

func TestTkPassthroughPendingSSE_WriteFailureReturnsFalseWithoutClearingPartialSemantics(t *testing.T) {
	p := newTkPassthroughPendingSSE(64)
	p.append("data: boom")
	ok := p.writeAndClear(&errWriter{}, 7)
	require.False(t, ok, "client disconnect must surface as false so call site sets clientDisconnected")
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, bytes.ErrTooLarge }

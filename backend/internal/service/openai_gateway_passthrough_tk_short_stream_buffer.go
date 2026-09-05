package service

import (
	"fmt"
	"io"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// tkPassthroughPendingSSE buffers leading SSE lines before the first client-visible
// output so a short/no-output failure can still failover safely.
type tkPassthroughPendingSSE struct {
	lines []string
	bytes int
	limit int
}

func newTkPassthroughPendingSSE(limit int) *tkPassthroughPendingSSE {
	return &tkPassthroughPendingSSE{
		lines: make([]string, 0, 8),
		limit: limit,
	}
}

func (p *tkPassthroughPendingSSE) append(line string) {
	p.lines = append(p.lines, line)
	p.bytes += len(line) + 1
}

// shouldRelease mirrors the previous closure: release only when a line starts
// client output; limit<=0 (or forceRelease) releases immediately; otherwise wait
// until buffered bytes reach the configured short-stream limit.
func (p *tkPassthroughPendingSSE) shouldRelease(lineStartsClientOutput bool, forceRelease bool) bool {
	if !lineStartsClientOutput {
		return false
	}
	if p.limit <= 0 || forceRelease {
		return true
	}
	return p.bytes >= p.limit
}

// writeAndClear writes buffered lines then clears. On write error it logs the
// same disconnect message as before and returns false (caller sets
// clientDisconnected). Does not Flush — Flush stays at the call site and is
// skipped after disconnect.
func (p *tkPassthroughPendingSSE) writeAndClear(w io.Writer, accountID int64) bool {
	for _, pending := range p.lines {
		if _, err := fmt.Fprintln(w, pending); err != nil {
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI passthrough] Client disconnected during streaming, continue draining upstream for usage: account=%d", accountID)
			return false
		}
	}
	p.lines = p.lines[:0]
	p.bytes = 0
	return true
}

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	// 15 分钟内 3 次能罩住大约每 5 分钟一次的持续截断，又不会被单次断流触发。
	geminiDisruptionWindow    = 15 * time.Minute
	geminiDisruptionThreshold = 3
	// INSUFFICIENT_G1_CREDITS 是账号额度耗尽，立刻停这一档，不走截断阶梯。
	geminiG1CreditsCooldown = 30 * time.Minute
)

// geminiDisruptionStore is the scheduling write this cooldown needs.
type geminiDisruptionStore interface {
	SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error
}

type geminiDisruptionState struct {
	hits        []time.Time
	tierCount   int
	tierUntil   time.Time
	cooledUntil time.Time
}

type geminiDisruptionRecorder struct {
	mu       sync.Mutex
	accounts map[int64]*geminiDisruptionState
}

// trip records one truncation. The 15-minute window must reach the threshold
// before a cooldown is considered. Repeated trips inside the Anthropic tier
// TTL climb anthropicCooldownTierLadder (30s, 2m, 10m). Events that arrive
// while that cooldown is still in force do not climb or rewrite it.
func (r *geminiDisruptionRecorder) trip(id int64, now time.Time) (apply bool, tierIndex int, cooldown time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.accounts == nil {
		r.accounts = map[int64]*geminiDisruptionState{}
	}
	st := r.accounts[id]
	if st == nil {
		st = &geminiDisruptionState{}
		r.accounts[id] = st
	}
	cutoff := now.Add(-geminiDisruptionWindow)
	kept := make([]time.Time, 0, len(st.hits)+1)
	for _, ts := range st.hits {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	kept = append(kept, now)
	st.hits = kept
	if len(kept) < geminiDisruptionThreshold {
		return false, 0, 0
	}
	st.hits = nil
	if !st.cooledUntil.IsZero() && !now.After(st.cooledUntil) {
		return false, 0, 0
	}
	tierTTL := time.Duration(anthropicCooldownTierTTLMinutes) * time.Minute
	if st.tierUntil.IsZero() || now.After(st.tierUntil) {
		st.tierCount = 0
	}
	st.tierCount++
	st.tierUntil = now.Add(tierTTL)
	tierIndex = st.tierCount - 1
	if tierIndex >= len(anthropicCooldownTierLadder) {
		tierIndex = len(anthropicCooldownTierLadder) - 1
	}
	cooldown = anthropicCooldownTierLadder[tierIndex]
	st.cooledUntil = now.Add(cooldown)
	return true, tierIndex, cooldown
}

var geminiDisruptions = &geminiDisruptionRecorder{}

func noteGeminiStreamDisruption(ctx context.Context, store geminiDisruptionStore, account *Account) {
	noteGeminiStreamDisruptionAt(ctx, store, account, time.Now())
}

func noteGeminiStreamDisruptionAt(ctx context.Context, store geminiDisruptionStore, account *Account, now time.Time) {
	if account == nil || account.ID == 0 {
		return
	}
	apply, tierIndex, cooldown := geminiDisruptions.trip(account.ID, now)
	if !apply {
		return
	}
	reason := fmt.Sprintf("gemini stream truncated or disconnected repeatedly (tier=%d cooldown=%s)", tierIndex, cooldown)
	coolGeminiAccount(ctx, store, account, now.Add(cooldown), reason)
}

func noteGeminiG1CreditsExhausted(ctx context.Context, store geminiDisruptionStore, account *Account) {
	noteGeminiG1CreditsExhaustedAt(ctx, store, account, time.Now())
}

func noteGeminiG1CreditsExhaustedAt(ctx context.Context, store geminiDisruptionStore, account *Account, now time.Time) {
	if account == nil || account.ID == 0 {
		return
	}
	coolGeminiAccount(ctx, store, account, now.Add(geminiG1CreditsCooldown), "antigravity INSUFFICIENT_G1_CREDITS")
}

func coolGeminiAccount(ctx context.Context, store geminiDisruptionStore, account *Account, until time.Time, reason string) {
	if store == nil || account == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := store.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("gemini_stream_disruption_temp_unsched_failed", "account_id", account.ID, "error", err)
		return
	}
	slog.Info("gemini_stream_disruption_temp_unschedulable", "account_id", account.ID, "until", until, "reason", reason)
}

func antigravityG1CreditsExhausted(body []byte) bool {
	return bytes.Contains(body, []byte("INSUFFICIENT_G1_CREDITS"))
}

func geminiUpstreamUnexpectedEOF(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "unexpected eof")
}

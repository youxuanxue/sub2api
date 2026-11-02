//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeStreakReader struct {
	mu      sync.Mutex
	results [][]Upstream400Streak // consumed per call; last one repeats
	err     error
	calls   int
	gotMin  int
	gotSpan time.Duration
}

func (r *fakeStreakReader) ListOpenUpstream400Streaks(_ context.Context, _ time.Time, minCount int, minSpan time.Duration) ([]Upstream400Streak, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gotMin, r.gotSpan = minCount, minSpan
	if r.err != nil {
		return nil, r.err
	}
	idx := r.calls
	if idx >= len(r.results) {
		idx = len(r.results) - 1
	}
	r.calls++
	return r.results[idx], nil
}

type fakeStreakAlerter struct {
	mu   sync.Mutex
	sent []Upstream400Streak
}

func (a *fakeStreakAlerter) NotifyUpstream400Streak(_ *Account, st Upstream400Streak) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sent = append(a.sent, st)
}

type fakeStreakHeartbeat struct{ last *OpsUpsertJobHeartbeatInput }

func (h *fakeStreakHeartbeat) UpsertJobHeartbeat(_ context.Context, in *OpsUpsertJobHeartbeatInput) error {
	h.last = in
	return nil
}

func streakFor(id int64, sig string) Upstream400Streak {
	return Upstream400Streak{AccountID: id, Signature: sig, SampleMessage: "Your account (#) does not have a valid AgentPlan subscription", Count: 25,
		FirstAt: timeFixed().Add(-40 * time.Minute), LastAt: timeFixed(), DistinctUsers: 1}
}

func newTestStreakSentinel(reader *fakeStreakReader, enabled bool) (*Upstream400StreakSentinel, *fakeStreakAlerter, *fakeStreakHeartbeat) {
	alerter := &fakeStreakAlerter{}
	hb := &fakeStreakHeartbeat{}
	s := NewUpstream400StreakSentinel(reader, nil, alerter, fakeFeishuCfg{enabled: enabled}, hb, nil)
	s.now = timeFixed
	return s, alerter, hb
}

func TestUpstream400StreakSentinel_AlertsOncePerStreakAndRearms(t *testing.T) {
	a, b := streakFor(116, "sig-expired"), streakFor(117, "sig-expired")
	reader := &fakeStreakReader{results: [][]Upstream400Streak{
		{a, b},                        // tick 1: both new → 2 cards
		{a, b},                        // tick 2: still open → no repeat
		{b},                           // tick 3: 116 recovered → re-armed, 117 still silent
		{a, b},                        // tick 4: 116 fails again → new card
		{streakFor(117, "sig-other")}, // tick 5: 117 changed failure shape → new card
	}}
	s, alerter, _ := newTestStreakSentinel(reader, true)
	ctx := context.Background()

	s.runOnce(ctx)
	require.Len(t, alerter.sent, 2)
	s.runOnce(ctx)
	require.Len(t, alerter.sent, 2, "open streak must not re-alert every tick")
	s.runOnce(ctx)
	require.Len(t, alerter.sent, 2)
	s.runOnce(ctx)
	require.Len(t, alerter.sent, 3, "a new streak after recovery alerts again")
	require.Equal(t, int64(116), alerter.sent[2].AccountID)
	s.runOnce(ctx)
	require.Len(t, alerter.sent, 4)
	require.Equal(t, "sig-other", alerter.sent[3].Signature)

	require.Equal(t, upstream400StreakMinCount, reader.gotMin)
	require.Equal(t, upstream400StreakMinSpan, reader.gotSpan)
}

func TestUpstream400StreakSentinel_FeishuDisabledSkipsQuery(t *testing.T) {
	reader := &fakeStreakReader{results: [][]Upstream400Streak{{streakFor(1, "x")}}}
	s, alerter, hb := newTestStreakSentinel(reader, false)
	s.runOnce(context.Background())
	require.Zero(t, reader.calls)
	require.Empty(t, alerter.sent)
	require.Nil(t, hb.last)
}

func TestUpstream400StreakSentinel_QueryErrorRecordsHeartbeatNoAlert(t *testing.T) {
	reader := &fakeStreakReader{err: errors.New("boom")}
	s, alerter, hb := newTestStreakSentinel(reader, true)
	s.runOnce(context.Background())
	require.Empty(t, alerter.sent)
	require.NotNil(t, hb.last)
	require.NotNil(t, hb.last.LastError)
	require.Contains(t, *hb.last.LastError, "boom")
}

func TestBuildUpstream400StreakText_AdvisoryOnly(t *testing.T) {
	acc := &Account{ID: 116, Name: "volcengine-agent-plan-2", Platform: PlatformNewAPI}
	body := buildUpstream400StreakText("prod", acc, streakFor(116, "s"), timeFixed())
	require.Contains(t, body, "volcengine-agent-plan-2")
	require.Contains(t, body, "**连续次数**：25")
	require.Contains(t, body, "**期间成功请求**：0")
	require.Contains(t, body, "40m0s")
	require.Contains(t, body, "本卡不改变账号状态")
	require.Contains(t, body, "AgentPlan", "internal ops card keeps the upstream text")

	long := streakFor(1, "s")
	long.SampleMessage = strings.Repeat("x", 1000)
	require.Less(t, len(buildUpstream400StreakText("prod", acc, long, timeFixed())), 1500, "sample is capped")
}

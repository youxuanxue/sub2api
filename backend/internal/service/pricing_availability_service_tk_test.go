//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// memoryAvailabilityRepo is an in-memory ModelAvailabilityRepository for tests.
// Mirrors the production ent-backed repo's Upsert semantics: serialize per cell.
type memoryAvailabilityRepo struct {
	mu   sync.Mutex
	rows map[string]AvailabilityState
}

func newMemoryRepo() *memoryAvailabilityRepo {
	return &memoryAvailabilityRepo{rows: map[string]AvailabilityState{}}
}

func (r *memoryAvailabilityRepo) key(p, m string) string { return p + "::" + m }

func (r *memoryAvailabilityRepo) Upsert(_ context.Context, p, m string, fn func(AvailabilityState) AvailabilityState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.rows[r.key(p, m)]
	r.rows[r.key(p, m)] = fn(cur)
	return nil
}

func (r *memoryAvailabilityRepo) Get(_ context.Context, p, m string) (AvailabilityState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rows[r.key(p, m)], nil
}

func newAvailabilityTestService(t *testing.T) (*PricingAvailabilityService, *memoryAvailabilityRepo, *fakeClock) {
	t.Helper()
	repo := newMemoryRepo()
	clk := &fakeClock{now: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)}
	svc := NewPricingAvailabilityService(repo, clk.Now)
	return svc, repo, clk
}

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time      { return f.now }
func (f *fakeClock) Advance(d time.Duration) { f.now = f.now.Add(d) }

// TestRecordOutcome_SuccessTransitionsToOK 钉住成功路径：untested → ok，
// sample_ok / sample_total / last_seen_ok 都填上。
func TestRecordOutcome_SuccessTransitionsToOK(t *testing.T) {
	ctx := context.Background()
	svc, repo, clk := newAvailabilityTestService(t)

	svc.RecordOutcome(ctx, AvailabilityOutcome{
		Platform: "gemini", ModelID: "gemini-2.5-pro",
		Success: true, UpstreamStatusCode: 200, AccountID: 7,
	})

	got, _ := repo.Get(ctx, "gemini", "gemini-2.5-pro")
	require.Equal(t, AvailabilityStatusOK, got.Status)
	require.Equal(t, 1, got.SampleOK24h)
	require.Equal(t, 1, got.SampleTotal24h)
	require.NotNil(t, got.LastSeenOKAt)
	require.Equal(t, clk.now, *got.LastSeenOKAt)
	require.Empty(t, got.LastFailureKind)
	require.NotNil(t, got.LastAccountID)
	require.Equal(t, int64(7), *got.LastAccountID)
}

// TestRecordOutcome_ModelNotFoundFlipsToUnreachable_SingleSample 钉住强信号：
// 上游 4xx + body 含 "model ... not found" → 单条样本立即翻 unreachable。
// 这是 PR #121 schema-cleanup 之外另一种"模型在 Google 那侧不可用"的运维信号。
func TestRecordOutcome_ModelNotFoundFlipsToUnreachable_SingleSample(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newAvailabilityTestService(t)

	svc.RecordOutcome(ctx, AvailabilityOutcome{
		Platform: "gemini", ModelID: "gemini-3.1-pro",
		Success: false, UpstreamStatusCode: 404,
		UpstreamErrorBody: `{"error":{"message":"Requested entity was not found.","code":"NOT_FOUND"}}`,
	})

	got, _ := repo.Get(ctx, "gemini", "gemini-3.1-pro")
	require.Equal(t, AvailabilityStatusUnreachable, got.Status)
	require.Equal(t, FailureKindModelNotFound, got.LastFailureKind)
	require.Equal(t, 1, got.SampleTotal24h)
	require.Equal(t, 0, got.SampleOK24h)
}

// TestRecordOutcome_RateLimited_DoesNotPolluteSamples 钉住§1.3 关键不变量：
// 429 / rate_limited 是账号级信号不是模型级，绝对不能让一次限流把模型从 ok
// 翻成 stale/unreachable。这是 2026-05-06 v1.7.19 deploy 时段 6 的 503 误判
// 背后的同类问题。
func TestRecordOutcome_RateLimited_DoesNotPolluteSamples(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newAvailabilityTestService(t)

	// 先攒 20 次成功，确认 status=ok 且 sample 计数到位
	for i := 0; i < 20; i++ {
		svc.RecordOutcome(ctx, AvailabilityOutcome{
			Platform: "gemini", ModelID: "gemini-2.5-flash",
			Success: true, UpstreamStatusCode: 200,
		})
	}
	got, _ := repo.Get(ctx, "gemini", "gemini-2.5-flash")
	require.Equal(t, AvailabilityStatusOK, got.Status)
	require.Equal(t, 20, got.SampleOK24h)
	require.Equal(t, 20, got.SampleTotal24h)

	// 注入一次 429 — 不应改 sample，也不应改 status
	svc.RecordOutcome(ctx, AvailabilityOutcome{
		Platform: "gemini", ModelID: "gemini-2.5-flash",
		Success: false, UpstreamStatusCode: 429,
		UpstreamErrorBody: `{"error":"rate limit exceeded"}`,
	})
	got, _ = repo.Get(ctx, "gemini", "gemini-2.5-flash")
	require.Equal(t, AvailabilityStatusOK, got.Status, "429 must not flip ok→non-ok")
	require.Equal(t, 20, got.SampleOK24h, "rate_limited must not increment sample_ok")
	require.Equal(t, 20, got.SampleTotal24h, "rate_limited must not increment sample_total")
	require.Equal(t, FailureKindRateLimited, got.LastFailureKind)
}

// TestRecordOutcome_AuthFailure_DoesNotPolluteSamples 同上：401/403 是账号级。
func TestRecordOutcome_AuthFailure_DoesNotPolluteSamples(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newAvailabilityTestService(t)

	for i := 0; i < 10; i++ {
		svc.RecordOutcome(ctx, AvailabilityOutcome{
			Platform: "openai", ModelID: "gpt-5.4",
			Success: true, UpstreamStatusCode: 200,
		})
	}
	svc.RecordOutcome(ctx, AvailabilityOutcome{
		Platform: "openai", ModelID: "gpt-5.4",
		Success: false, UpstreamStatusCode: 401,
		UpstreamErrorBody: `{"error":"invalid_api_key"}`,
	})
	got, _ := repo.Get(context.Background(), "openai", "gpt-5.4")
	require.Equal(t, AvailabilityStatusOK, got.Status)
	require.Equal(t, 10, got.SampleTotal24h)
	require.Equal(t, FailureKindAuthFailure, got.LastFailureKind)
}

// TestRecordOutcome_5xx_SoftSignal 5xx 累计影响 status，但单条不立即翻。
func TestRecordOutcome_5xx_SoftSignal(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newAvailabilityTestService(t)

	// 先攒 90 次成功
	for i := 0; i < 90; i++ {
		svc.RecordOutcome(ctx, AvailabilityOutcome{
			Platform: "anthropic", ModelID: "claude-opus-4-5",
			Success: true, UpstreamStatusCode: 200,
		})
	}
	// 注入 6 次 5xx — sample_total=96, sample_ok=90, rate=0.9375 → stale (不在 ok 阈值)
	for i := 0; i < 6; i++ {
		svc.RecordOutcome(ctx, AvailabilityOutcome{
			Platform: "anthropic", ModelID: "claude-opus-4-5",
			Success: false, UpstreamStatusCode: 503,
			UpstreamErrorBody: `{"error":"service unavailable"}`,
		})
	}
	got, _ := repo.Get(ctx, "anthropic", "claude-opus-4-5")
	require.Equal(t, 96, got.SampleTotal24h)
	require.Equal(t, 90, got.SampleOK24h)
	require.InDelta(t, 0.9375, got.SuccessRate24h(), 0.0001)
	require.Equal(t, AvailabilityStatusStale, got.Status)
	require.Equal(t, FailureKindUpstream5xx, got.LastFailureKind)

	// 再多注入 5xx 把 rate 拉到 <80% → unreachable
	for i := 0; i < 30; i++ {
		svc.RecordOutcome(ctx, AvailabilityOutcome{
			Platform: "anthropic", ModelID: "claude-opus-4-5",
			Success: false, UpstreamStatusCode: 502,
		})
	}
	got, _ = repo.Get(ctx, "anthropic", "claude-opus-4-5")
	// sample_total=126 sample_ok=90 rate=0.7142 < 0.80 → unreachable
	require.Less(t, got.SuccessRate24h(), AvailabilityUnreachableThreshold)
	require.Equal(t, AvailabilityStatusUnreachable, got.Status)
}

// TestRecordOutcome_NetworkError_AsSoftSignal 网络错误（DNS/timeout/TLS）
// 走 upstream_5xx 同档（soft signal），不是 inconclusive。
func TestRecordOutcome_NetworkError_AsSoftSignal(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := newAvailabilityTestService(t)

	svc.RecordOutcome(ctx, AvailabilityOutcome{
		Platform: "gemini", ModelID: "gemini-2.0-flash",
		Success: false, NetworkError: true,
	})
	got, _ := repo.Get(ctx, "gemini", "gemini-2.0-flash")
	require.Equal(t, FailureKindNetworkError, got.LastFailureKind)
	require.Equal(t, 1, got.SampleTotal24h, "network_error counts as a sample (soft signal)")
}

// TestRecordOutcome_RollingWindow_24hReset 钉住 24h rolling window：跨 24h
// 边界后，sample 计数归零。这是为了不让旧的失败记录长期把 status 锁在
// non-ok。
func TestRecordOutcome_RollingWindow_24hReset(t *testing.T) {
	ctx := context.Background()
	svc, repo, clk := newAvailabilityTestService(t)

	// Day 1: 10 个失败（5xx）
	for i := 0; i < 10; i++ {
		svc.RecordOutcome(ctx, AvailabilityOutcome{
			Platform: "gemini", ModelID: "gemini-3.1-pro-preview",
			Success: false, UpstreamStatusCode: 503,
		})
	}
	got, _ := repo.Get(ctx, "gemini", "gemini-3.1-pro-preview")
	require.Equal(t, 10, got.SampleTotal24h)
	require.Equal(t, AvailabilityStatusUnreachable, got.Status)

	// 推进 25 小时 — window 应重置
	clk.Advance(25 * time.Hour)

	// Day 2: 1 次成功 — 应看到 sample 重置后只有 1 ok / 1 total
	svc.RecordOutcome(ctx, AvailabilityOutcome{
		Platform: "gemini", ModelID: "gemini-3.1-pro-preview",
		Success: true, UpstreamStatusCode: 200,
	})
	got, _ = repo.Get(ctx, "gemini", "gemini-3.1-pro-preview")
	require.Equal(t, 1, got.SampleOK24h, "rolling window must reset")
	require.Equal(t, 1, got.SampleTotal24h)
	require.Equal(t, AvailabilityStatusOK, got.Status)
}

// TestRecordOutcome_OkBecomesStaleAfter24h 钉住 last_seen_ok_at staleness 推导：
// 即使 24h 内成功率 100%，但 last_seen_ok_at >24h ago 时也应翻 stale。
// 对应 cold-tail 场景 —— 上次成功在 25h 前，从那之后一个样本都没（包括无失败），
// 这种 cell 主动探测进 24h 周期重置后也合理地表现为 stale。
func TestRecordOutcome_OkBecomesStaleAfter24h(t *testing.T) {
	ctx := context.Background()
	svc, repo, clk := newAvailabilityTestService(t)

	svc.RecordOutcome(ctx, AvailabilityOutcome{
		Platform: "gemini", ModelID: "gemini-2.5-flash",
		Success: true, UpstreamStatusCode: 200,
	})
	got, _ := repo.Get(ctx, "gemini", "gemini-2.5-flash")
	require.Equal(t, AvailabilityStatusOK, got.Status)

	// 推进 25h，注入又一次成功 — window 重置后 sample_ok=1 sample_total=1
	// last_seen_ok_at 更新为 now，所以 status 应仍为 ok（最新成功）
	clk.Advance(25 * time.Hour)
	svc.RecordOutcome(ctx, AvailabilityOutcome{
		Platform: "gemini", ModelID: "gemini-2.5-flash",
		Success: true, UpstreamStatusCode: 200,
	})
	got, _ = repo.Get(ctx, "gemini", "gemini-2.5-flash")
	require.Equal(t, AvailabilityStatusOK, got.Status, "fresh success after 25h must restore ok")
}

// TestClassifyFailureKind_Matrix 钉住 §1.3 分类矩阵的每条规则。
func TestClassifyFailureKind_Matrix(t *testing.T) {
	cases := []struct {
		name     string
		outcome  AvailabilityOutcome
		expected string
	}{
		{"network_error", AvailabilityOutcome{NetworkError: true}, FailureKindNetworkError},
		{"429_status", AvailabilityOutcome{UpstreamStatusCode: 429}, FailureKindRateLimited},
		{"401_status", AvailabilityOutcome{UpstreamStatusCode: 401}, FailureKindAuthFailure},
		{"403_status", AvailabilityOutcome{UpstreamStatusCode: 403}, FailureKindAuthFailure},
		{
			name:     "rate_limit_in_body",
			outcome:  AvailabilityOutcome{UpstreamStatusCode: 400, UpstreamErrorBody: `{"error":"You hit the rate limit."}`},
			expected: FailureKindRateLimited,
		},
		{
			name:     "quota_in_body",
			outcome:  AvailabilityOutcome{UpstreamStatusCode: 400, UpstreamErrorBody: `Daily quota exceeded`},
			expected: FailureKindRateLimited,
		},
		{
			name:     "model_not_found_404",
			outcome:  AvailabilityOutcome{UpstreamStatusCode: 404, UpstreamErrorBody: `model gemini-x not found`},
			expected: FailureKindModelNotFound,
		},
		{
			name:     "model_requested_entity_not_found",
			outcome:  AvailabilityOutcome{UpstreamStatusCode: 400, UpstreamErrorBody: `Requested entity was not found. model: gemini-3.1-pro`},
			expected: FailureKindModelNotFound,
		},
		{
			name:     "codex_model_not_supported",
			outcome:  AvailabilityOutcome{UpstreamStatusCode: 400, UpstreamErrorBody: `The 'gpt-4o-mini' model is not supported when using Codex with a ChatGPT account.`},
			expected: FailureKindModelNotFound,
		},
		{
			name:     "404_no_model_text",
			outcome:  AvailabilityOutcome{UpstreamStatusCode: 404, UpstreamErrorBody: `<html>Not Found</html>`},
			expected: FailureKindNotFound,
		},
		{"503_status", AvailabilityOutcome{UpstreamStatusCode: 503}, FailureKindUpstream5xx},
		{"500_status", AvailabilityOutcome{UpstreamStatusCode: 500}, FailureKindUpstream5xx},
		{"200_failure_bad_shape", AvailabilityOutcome{UpstreamStatusCode: 200}, FailureKindBadRespShape},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyFailureKind(tc.outcome)
			require.Equal(t, tc.expected, got)
		})
	}
}

// TestRecordOutcome_NilSafety 钉住 nil-safe 边界：service nil / repo nil /
// 空 platform / 空 model 都不能 panic。
func TestRecordOutcome_NilSafety(t *testing.T) {
	ctx := context.Background()
	var svcNil *PricingAvailabilityService
	require.NotPanics(t, func() {
		svcNil.RecordOutcome(ctx, AvailabilityOutcome{Platform: "gemini", ModelID: "x"})
	})

	svc := NewPricingAvailabilityService(nil, nil)
	require.NotPanics(t, func() {
		svc.RecordOutcome(ctx, AvailabilityOutcome{Platform: "gemini", ModelID: "x"})
	})

	svc2, _, _ := newAvailabilityTestService(t)
	require.NotPanics(t, func() {
		svc2.RecordOutcome(ctx, AvailabilityOutcome{Platform: "", ModelID: "x"})
		svc2.RecordOutcome(ctx, AvailabilityOutcome{Platform: "gemini", ModelID: ""})
	})
}

// TestGetAvailability_Untested 没写过的 cell 返回 Status="" zero-value。
// catalog handler 把空 status 映射为 "untested" 给前端。
func TestGetAvailability_Untested(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newAvailabilityTestService(t)
	got, err := svc.GetAvailability(ctx, "gemini", "gemini-never-seen")
	require.NoError(t, err)
	require.Empty(t, got.Status)
}

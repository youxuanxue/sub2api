package service

import (
	"context"
	"testing"
	"time"
)

// countingUsageFetcher 记录上游 usage 抓取次数，用于断言「手动查询是否真打上游」。
type countingUsageFetcher struct {
	calls int
	resp  *ClaudeUsageResponse
}

func (f *countingUsageFetcher) FetchUsage(context.Context, string, string) (*ClaudeUsageResponse, error) {
	f.calls++
	return f.resp, nil
}

func (f *countingUsageFetcher) FetchUsageWithOptions(context.Context, *ClaudeUsageFetchOptions) (*ClaudeUsageResponse, error) {
	f.calls++
	return f.resp, nil
}

// forceRefreshRepo 在 stubOpenAIAccountRepo（按 accounts slice 提供 GetByID）之上补齐
// GetUsage anthropic 路径会触达的写回方法，避免 nil-interface panic。
type forceRefreshRepo struct {
	stubOpenAIAccountRepo
}

func (forceRefreshRepo) UpdateExtra(context.Context, int64, map[string]any) error       { return nil }
func (forceRefreshRepo) UpdateSessionWindowEnd(context.Context, int64, time.Time) error { return nil }

// TestAccountUsageService_GetUsage_AnthropicForceBypassesCache 钉死 anthropic OAuth 账号
// 的「查询」按钮（force=true）必须绕过 3min 正缓存强制刷新——这是 41e7ae53 修了 OpenAI
// 却漏给 anthropic 的同一个 bug。force=false 仍读缓存（保护上游 usage 端点）。
func TestAccountUsageService_GetUsage_AnthropicForceBypassesCache(t *testing.T) {
	account := Account{
		ID:       7777,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token": "tkn-abc",
		},
	}

	fresh := &ClaudeUsageResponse{}
	fresh.FiveHour.Utilization = 42 // 上游「新」值

	fetcher := &countingUsageFetcher{resp: fresh}
	cache := NewUsageCache()

	// 预置一条「新鲜」正缓存（3min 内），内容是旧值；force 必须绕过它。
	stale := &ClaudeUsageResponse{}
	stale.FiveHour.Utilization = 99 // 缓存里的「旧」值
	cache.apiCache.Store(account.ID, &apiUsageCache{response: stale, timestamp: time.Now()})
	// 预置窗口统计缓存，使 addWindowStats 不触达 usageLogRepo（保持 nil）。
	cache.windowStatsCache.Store(account.ID, &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})

	svc := &AccountUsageService{
		accountRepo:  &forceRefreshRepo{stubOpenAIAccountRepo{accounts: []Account{account}}},
		usageFetcher: fetcher,
		cache:        cache,
		// tlsFPProfileService / identityCache / usageLogRepo 保持 nil：
		// 账号无 Extra → IsTLSFingerprintEnabled=false → ResolveTLSProfile 不解引用 nil；
		// identityCache 有 nil 守卫；windowStatsCache 预置使 usageLogRepo 不被触达。
	}

	ctx := context.Background()

	// force=false：命中正缓存，返回旧值，不打上游。
	u1, err := svc.GetUsage(ctx, account.ID, false)
	if err != nil {
		t.Fatalf("GetUsage(force=false) error = %v", err)
	}
	if fetcher.calls != 0 {
		t.Fatalf("force=false 不应打上游，实际 calls=%d", fetcher.calls)
	}
	if u1.FiveHour == nil || u1.FiveHour.Utilization != 99 {
		t.Fatalf("force=false 应返回缓存旧值 99，实际 %#v", u1.FiveHour)
	}

	// force=true：绕过正缓存，强制打上游一次，返回新值。
	u2, err := svc.GetUsage(ctx, account.ID, true)
	if err != nil {
		t.Fatalf("GetUsage(force=true) error = %v", err)
	}
	if fetcher.calls != 1 {
		t.Fatalf("force=true 应绕缓存打上游恰好 1 次，实际 calls=%d", fetcher.calls)
	}
	if u2.FiveHour == nil || u2.FiveHour.Utilization != 42 {
		t.Fatalf("force=true 应返回上游新值 42，实际 %#v", u2.FiveHour)
	}
}

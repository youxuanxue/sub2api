package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const testSameAccountRetryLimit = 3

// ---------------------------------------------------------------------------
// Mock
// ---------------------------------------------------------------------------

// mockTempUnscheduler 记录 TempUnscheduleRetryableError 的调用信息。
type mockTempUnscheduler struct {
	calls []tempUnscheduleCall
}

type tempUnscheduleCall struct {
	accountID   int64
	failoverErr *service.UpstreamFailoverError
}

func (m *mockTempUnscheduler) TempUnscheduleRetryableError(_ context.Context, accountID int64, failoverErr *service.UpstreamFailoverError) {
	m.calls = append(m.calls, tempUnscheduleCall{accountID: accountID, failoverErr: failoverErr})
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newTestFailoverErr(statusCode int, retryable, forceBilling bool) *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:             statusCode,
		RetryableOnSameAccount: retryable,
		ForceCacheBilling:      forceBilling,
	}
}

// newTestFailoverErrWithBody 构造带 ResponseBody 的 UpstreamFailoverError，用于覆盖
// TK fail-fast 路径（HandleFailoverError 中 statusCode=403 + 非 anthropic JSON body 分支）。
func newTestFailoverErrWithBody(statusCode int, retryable bool, body []byte) *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{
		StatusCode:             statusCode,
		RetryableOnSameAccount: retryable,
		ResponseBody:           body,
	}
}

// ---------------------------------------------------------------------------
// NewFailoverState 测试
// ---------------------------------------------------------------------------

func TestNewFailoverState(t *testing.T) {
	t.Run("初始化字段正确", func(t *testing.T) {
		fs := NewFailoverState(5, true)
		require.Equal(t, 5, fs.MaxSwitches)
		require.Equal(t, 0, fs.SwitchCount)
		require.NotNil(t, fs.FailedAccountIDs)
		require.Empty(t, fs.FailedAccountIDs)
		require.NotNil(t, fs.SameAccountRetryCount)
		require.Empty(t, fs.SameAccountRetryCount)
		require.Nil(t, fs.LastFailoverErr)
		require.False(t, fs.ForceCacheBilling)
		require.True(t, fs.hasBoundSession)
	})

	t.Run("无绑定会话", func(t *testing.T) {
		fs := NewFailoverState(3, false)
		require.Equal(t, 3, fs.MaxSwitches)
		require.False(t, fs.hasBoundSession)
	})

	t.Run("零最大切换次数", func(t *testing.T) {
		fs := NewFailoverState(0, false)
		require.Equal(t, 0, fs.MaxSwitches)
	})
}

// ---------------------------------------------------------------------------
// sleepWithContext 测试
// ---------------------------------------------------------------------------

func TestSleepWithContext(t *testing.T) {
	t.Run("零时长立即返回true", func(t *testing.T) {
		start := time.Now()
		ok := sleepWithContext(context.Background(), 0)
		require.True(t, ok)
		require.Less(t, time.Since(start), 50*time.Millisecond)
	})

	t.Run("负时长立即返回true", func(t *testing.T) {
		start := time.Now()
		ok := sleepWithContext(context.Background(), -1*time.Second)
		require.True(t, ok)
		require.Less(t, time.Since(start), 50*time.Millisecond)
	})

	t.Run("正常等待后返回true", func(t *testing.T) {
		start := time.Now()
		ok := sleepWithContext(context.Background(), 50*time.Millisecond)
		elapsed := time.Since(start)
		require.True(t, ok)
		require.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
		require.Less(t, elapsed, 500*time.Millisecond)
	})

	t.Run("已取消context立即返回false", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		ok := sleepWithContext(ctx, 5*time.Second)
		require.False(t, ok)
		require.Less(t, time.Since(start), 50*time.Millisecond)
	})

	t.Run("等待期间context取消返回false", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		ok := sleepWithContext(ctx, 5*time.Second)
		elapsed := time.Since(start)
		require.False(t, ok)
		require.Less(t, elapsed, 500*time.Millisecond)
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — 基本切换流程
// ---------------------------------------------------------------------------

func TestHandleFailoverError_BasicSwitch(t *testing.T) {
	t.Run("显式停止不切换账号且旧错误默认仍切换", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		stopErr := &service.UpstreamFailoverError{
			Stage:             service.GatewayFailureStageAccountAuth,
			Scope:             service.GatewayFailureScopeProvider,
			NextAccountAction: service.NextAccountStop,
		}

		action := fs.HandleFailoverError(context.Background(), mock, 100, service.PlatformGrok, testSameAccountRetryLimit, stopErr)

		require.Equal(t, FailoverExhausted, action)
		require.Zero(t, fs.SwitchCount)
		require.Empty(t, fs.FailedAccountIDs)
		require.Equal(t, stopErr, fs.LastFailoverErr)

		legacyErr := newTestFailoverErr(http.StatusTooManyRequests, false, false)
		action = fs.HandleFailoverError(context.Background(), mock, 100, service.PlatformGrok, testSameAccountRetryLimit, legacyErr)

		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)
		require.Contains(t, fs.FailedAccountIDs, int64(100))
	})

	t.Run("已取消的认证失败不改变切换状态", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := &service.UpstreamFailoverError{
			Stage:             service.GatewayFailureStageAccountAuth,
			Scope:             service.GatewayFailureScopeAccount,
			NextAccountAction: service.NextAccountRetry,
		}

		action := fs.HandleFailoverError(ctx, mock, 101, service.PlatformGrok, testSameAccountRetryLimit, err)

		require.Equal(t, FailoverCanceled, action)
		require.Zero(t, fs.SwitchCount)
		require.Empty(t, fs.FailedAccountIDs)
		require.Nil(t, fs.LastFailoverErr)
		require.Empty(t, mock.calls)
	})

	t.Run("非重试错误_非Antigravity_直接切换", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(500, false, false)

		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)

		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)
		require.Contains(t, fs.FailedAccountIDs, int64(100))
		require.Equal(t, err, fs.LastFailoverErr)
		require.False(t, fs.ForceCacheBilling)
		require.Empty(t, mock.calls, "不应调用 TempUnschedule")
	})

	t.Run("非重试错误_Antigravity_第一次切换无延迟", func(t *testing.T) {
		// switchCount 从 0→1 时，sleepFailoverDelay(ctx, 1) 的延时 = (1-1)*1s = 0
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(500, false, false)

		start := time.Now()
		action := fs.HandleFailoverError(context.Background(), mock, 100, service.PlatformAntigravity, 3, err)
		elapsed := time.Since(start)

		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)
		require.Less(t, elapsed, 200*time.Millisecond, "第一次切换延迟应为 0")
	})

	t.Run("非重试错误_Antigravity_第二次切换有1秒延迟", func(t *testing.T) {
		// switchCount 从 1→2 时，sleepFailoverDelay(ctx, 2) 的延时 = (2-1)*1s = 1s
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		fs.SwitchCount = 1 // 模拟已切换一次

		err := newTestFailoverErr(500, false, false)
		start := time.Now()
		action := fs.HandleFailoverError(context.Background(), mock, 200, service.PlatformAntigravity, 3, err)
		elapsed := time.Since(start)

		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 2, fs.SwitchCount)
		require.GreaterOrEqual(t, elapsed, 800*time.Millisecond, "第二次切换延迟应约 1s")
		require.Less(t, elapsed, 3*time.Second)
	})

	t.Run("连续切换直到耗尽", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(2, false)

		// 第一次切换：0→1
		err1 := newTestFailoverErr(500, false, false)
		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err1)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)

		// 第二次切换：1→2
		err2 := newTestFailoverErr(502, false, false)
		action = fs.HandleFailoverError(context.Background(), mock, 200, "openai", 3, err2)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 2, fs.SwitchCount)

		// 第三次已耗尽：SwitchCount(2) >= MaxSwitches(2)
		err3 := newTestFailoverErr(503, false, false)
		action = fs.HandleFailoverError(context.Background(), mock, 300, "openai", 3, err3)
		require.Equal(t, FailoverExhausted, action)
		require.Equal(t, 2, fs.SwitchCount, "耗尽时不应继续递增")

		// 验证失败账号列表
		require.Len(t, fs.FailedAccountIDs, 3)
		require.Contains(t, fs.FailedAccountIDs, int64(100))
		require.Contains(t, fs.FailedAccountIDs, int64(200))
		require.Contains(t, fs.FailedAccountIDs, int64(300))

		// LastFailoverErr 应为最后一次的错误
		require.Equal(t, err3, fs.LastFailoverErr)
	})

	t.Run("MaxSwitches为0时首次即耗尽", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(0, false)
		err := newTestFailoverErr(500, false, false)

		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.Equal(t, FailoverExhausted, action)
		require.Equal(t, 0, fs.SwitchCount)
		require.Contains(t, fs.FailedAccountIDs, int64(100))
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — 缓存计费 (ForceCacheBilling)
// ---------------------------------------------------------------------------

func TestHandleFailoverError_CacheBilling(t *testing.T) {
	t.Run("hasBoundSession为true且实际切换时设置ForceCacheBilling", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, true) // hasBoundSession=true
		err := newTestFailoverErr(500, false, false)

		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.True(t, fs.ForceCacheBilling)
	})

	t.Run("同账号重试时仅凭hasBoundSession不设置ForceCacheBilling", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, true)
		err := newTestFailoverErr(400, true, false)

		fs.HandleFailoverError(context.Background(), mock, 100, "openai", maxSameAccountRetries, err)

		require.False(t, fs.ForceCacheBilling)
		require.Zero(t, fs.SwitchCount)
	})

	t.Run("同账号重试耗尽并实际切换时设置ForceCacheBilling", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, true)
		err := newTestFailoverErr(400, true, false)

		for i := 0; i < maxSameAccountRetries; i++ {
			fs.HandleFailoverError(context.Background(), mock, 100, "openai", maxSameAccountRetries, err)
			require.False(t, fs.ForceCacheBilling)
		}
		fs.HandleFailoverError(context.Background(), mock, 100, "openai", maxSameAccountRetries, err)

		require.True(t, fs.ForceCacheBilling)
		require.Equal(t, 1, fs.SwitchCount)
	})

	t.Run("failoverErr.ForceCacheBilling为true时设置", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(500, false, true) // ForceCacheBilling=true

		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.True(t, fs.ForceCacheBilling)
	})

	t.Run("同账号重试保留显式ForceCacheBilling", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, true)
		err := newTestFailoverErr(400, true, true)

		fs.HandleFailoverError(context.Background(), mock, 100, "openai", maxSameAccountRetries, err)

		require.True(t, fs.ForceCacheBilling)
		require.Zero(t, fs.SwitchCount)
	})

	t.Run("两者均为false时不设置", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(500, false, false)

		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.False(t, fs.ForceCacheBilling)
	})

	t.Run("一旦设置不会被后续错误重置", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)

		// 第一次：ForceCacheBilling=true → 设置
		err1 := newTestFailoverErr(500, false, true)
		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err1)
		require.True(t, fs.ForceCacheBilling)

		// 第二次：ForceCacheBilling=false → 仍然保持 true
		err2 := newTestFailoverErr(502, false, false)
		fs.HandleFailoverError(context.Background(), mock, 200, "openai", 3, err2)
		require.True(t, fs.ForceCacheBilling, "ForceCacheBilling 一旦设置不应被重置")
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — 同账号重试 (RetryableOnSameAccount)
// ---------------------------------------------------------------------------

func TestHandleFailoverError_SameAccountRetry(t *testing.T) {
	t.Run("第一次重试返回FailoverContinue", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(400, true, false)

		start := time.Now()
		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		elapsed := time.Since(start)

		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SameAccountRetryCount[100])
		require.Equal(t, 0, fs.SwitchCount, "同账号重试不应增加切换计数")
		require.NotContains(t, fs.FailedAccountIDs, int64(100), "同账号重试不应加入失败列表")
		require.Empty(t, mock.calls, "同账号重试期间不应调用 TempUnschedule")
		// 验证等待了 sameAccountRetryDelay (500ms)
		require.GreaterOrEqual(t, elapsed, 400*time.Millisecond)
		require.Less(t, elapsed, 2*time.Second)
	})

	t.Run("达到最大重试次数前均返回FailoverContinue", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(400, true, false)

		for i := 1; i <= 3; i++ {
			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
			require.Equal(t, FailoverContinue, action)
			require.Equal(t, i, fs.SameAccountRetryCount[100])
		}

		require.Empty(t, mock.calls, "达到最大重试次数前均不应调用 TempUnschedule")
	})

	t.Run("超过最大重试次数后触发TempUnschedule并切换", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(400, true, false)

		for i := 0; i < 3; i++ {
			fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		}
		require.Equal(t, 3, fs.SameAccountRetryCount[100])

		// 第 3+1 次：重试耗尽，应切换账号
		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)
		require.Contains(t, fs.FailedAccountIDs, int64(100))

		// 验证 TempUnschedule 被调用
		require.Len(t, mock.calls, 1)
		require.Equal(t, int64(100), mock.calls[0].accountID)
		require.Equal(t, err, mock.calls[0].failoverErr)
	})

	t.Run("不同账号独立跟踪重试次数", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(5, false)
		err := newTestFailoverErr(400, true, false)

		// 账号 100 第一次重试
		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SameAccountRetryCount[100])

		// 账号 200 第一次重试（独立计数）
		action = fs.HandleFailoverError(context.Background(), mock, 200, "openai", 3, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SameAccountRetryCount[200])
		require.Equal(t, 1, fs.SameAccountRetryCount[100], "账号 100 的计数不应受影响")
	})

	t.Run("重试耗尽后再次遇到同账号_直接切换", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(5, false)
		err := newTestFailoverErr(400, true, false)

		// 耗尽账号 100 的重试
		for i := 0; i < 3; i++ {
			fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		}
		// 第 3+1 次: 重试耗尽 → 切换
		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.Equal(t, FailoverContinue, action)

		// 再次遇到账号 100，计数仍为 3，条件不满足 → 直接切换
		action = fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.Equal(t, FailoverContinue, action)
		require.Len(t, mock.calls, 2, "第二次耗尽也应调用 TempUnschedule")
	})

	t.Run("尊重账号级retryLimit_配置1次只重试1次", func(t *testing.T) {
		// 回归测试：Anthropic 等路径此前硬编码同账号重试 3 次，忽略账号
		// pool_mode_retry_count 配置。此处验证传入 retryLimit=1 时只重试 1 次即切换。
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(5, false)
		err := newTestFailoverErr(502, true, false)
		const retryLimit = 1

		// 第 1 次：同账号重试
		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", retryLimit, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SameAccountRetryCount[100])
		require.Equal(t, 0, fs.SwitchCount, "首次重试不应切换账号")
		require.Empty(t, mock.calls, "未耗尽前不应 TempUnschedule")

		// 第 2 次：已达上限 1 → 不再同账号重试，直接切换 + TempUnschedule
		action = fs.HandleFailoverError(context.Background(), mock, 100, "openai", retryLimit, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SameAccountRetryCount[100], "重试计数不应超过 retryLimit")
		require.Equal(t, 1, fs.SwitchCount, "重试耗尽应切换账号")
		require.Contains(t, fs.FailedAccountIDs, int64(100))
		require.Len(t, mock.calls, 1, "重试耗尽应触发 TempUnschedule")
	})

	t.Run("retryLimit为0时立即切换不重试", func(t *testing.T) {
		// pool_mode_retry_count=0 表示关闭同账号重试（如 GPT Image 账号）。
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(5, false)
		err := newTestFailoverErr(502, true, false)

		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 0, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 0, fs.SameAccountRetryCount[100], "retryLimit=0 不应发生同账号重试")
		require.Equal(t, 1, fs.SwitchCount, "应立即切换账号")
		require.Len(t, mock.calls, 1, "应立即 TempUnschedule")
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — TempUnschedule 调用验证
// ---------------------------------------------------------------------------

func TestHandleFailoverError_TempUnschedule(t *testing.T) {
	t.Run("非重试错误不调用TempUnschedule", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(500, false, false) // RetryableOnSameAccount=false

		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.Empty(t, mock.calls)
	})

	t.Run("重试错误耗尽后调用TempUnschedule_传入正确参数", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(502, true, false)

		for i := 0; i < 3; i++ {
			fs.HandleFailoverError(context.Background(), mock, 42, "openai", 3, err)
		}
		// 再次触发时才会执行 TempUnschedule + 切换
		fs.HandleFailoverError(context.Background(), mock, 42, "openai", 3, err)

		require.Len(t, mock.calls, 1)
		require.Equal(t, int64(42), mock.calls[0].accountID)
		require.Equal(t, 502, mock.calls[0].failoverErr.StatusCode)
		require.True(t, mock.calls[0].failoverErr.RetryableOnSameAccount)
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — Context 取消
// ---------------------------------------------------------------------------

func TestHandleFailoverError_ContextCanceled(t *testing.T) {
	t.Run("同账号重试sleep期间context取消", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(400, true, false)

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel() // 通过入口检查后、sleep 期间取消
		}()

		start := time.Now()
		action := fs.HandleFailoverError(ctx, mock, 100, "openai", 3, err)
		elapsed := time.Since(start)

		require.Equal(t, FailoverCanceled, action)
		require.Less(t, elapsed, 100*time.Millisecond, "应立即返回")
		// 入口已取消时不得改变任何 failover 状态。
		require.Equal(t, 0, fs.SwitchCount, "取消的请求不应计入切换")
		require.Equal(t, 0, fs.SameAccountRetryCount[100], "取消的请求不应改动重试计数")
		require.NotContains(t, fs.FailedAccountIDs, int64(100))
		require.Nil(t, fs.LastFailoverErr)
		require.Empty(t, mock.calls, "不应触发 TempUnschedule")
	})

	t.Run("Antigravity延迟期间context取消", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		fs.SwitchCount = 1 // 下一次 switchCount=2 → delay = 1s
		err := newTestFailoverErr(500, false, false)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // 立即取消

		start := time.Now()
		action := fs.HandleFailoverError(ctx, mock, 100, service.PlatformAntigravity, 3, err)
		elapsed := time.Since(start)

		require.Equal(t, FailoverCanceled, action)
		require.Less(t, elapsed, 100*time.Millisecond, "应立即返回而非等待 1s")
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — FailedAccountIDs 跟踪
// ---------------------------------------------------------------------------

func TestHandleFailoverError_FailedAccountIDs(t *testing.T) {
	t.Run("切换时添加到失败列表", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)

		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, newTestFailoverErr(500, false, false))
		require.Contains(t, fs.FailedAccountIDs, int64(100))

		fs.HandleFailoverError(context.Background(), mock, 200, "openai", 3, newTestFailoverErr(502, false, false))
		require.Contains(t, fs.FailedAccountIDs, int64(200))
		require.Len(t, fs.FailedAccountIDs, 2)
	})

	t.Run("耗尽时也添加到失败列表", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(0, false)

		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, newTestFailoverErr(500, false, false))
		require.Equal(t, FailoverExhausted, action)
		require.Contains(t, fs.FailedAccountIDs, int64(100))
	})

	t.Run("同账号重试期间不添加到失败列表", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)

		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, newTestFailoverErr(400, true, false))
		require.Equal(t, FailoverContinue, action)
		require.NotContains(t, fs.FailedAccountIDs, int64(100))
	})

	t.Run("同一账号多次切换不重复添加", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(5, false)

		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, newTestFailoverErr(500, false, false))
		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, newTestFailoverErr(500, false, false))
		require.Len(t, fs.FailedAccountIDs, 1, "map 天然去重")
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — LastFailoverErr 更新
// ---------------------------------------------------------------------------

func TestHandleFailoverError_LastFailoverErr(t *testing.T) {
	t.Run("每次调用都更新LastFailoverErr", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)

		err1 := newTestFailoverErr(500, false, false)
		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err1)
		require.Equal(t, err1, fs.LastFailoverErr)

		err2 := newTestFailoverErr(502, false, false)
		fs.HandleFailoverError(context.Background(), mock, 200, "openai", 3, err2)
		require.Equal(t, err2, fs.LastFailoverErr)
	})

	t.Run("同账号重试时也更新LastFailoverErr", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)

		err := newTestFailoverErr(400, true, false)
		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.Equal(t, err, fs.LastFailoverErr)
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — 综合集成场景
// ---------------------------------------------------------------------------

func TestHandleFailoverError_IntegrationScenario(t *testing.T) {
	t.Run("模拟完整failover流程_多账号混合重试与切换", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, true) // hasBoundSession=true

		// 1. 账号 100 遇到可重试错误，同账号重试 3 次
		retryErr := newTestFailoverErr(400, true, false)
		for i := 0; i < 3; i++ {
			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, retryErr)
			require.Equal(t, FailoverContinue, action)
			require.False(t, fs.ForceCacheBilling, "同账号重试期间不应仅因绑定会话强制缓存计费")
		}

		// 2. 账号 100 超过重试上限 → TempUnschedule + 切换
		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, retryErr)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)
		require.True(t, fs.ForceCacheBilling, "实际切换账号时应设置 ForceCacheBilling")
		require.Len(t, mock.calls, 1)

		// 3. 账号 200 遇到不可重试错误 → 直接切换
		switchErr := newTestFailoverErr(500, false, false)
		action = fs.HandleFailoverError(context.Background(), mock, 200, "openai", 3, switchErr)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 2, fs.SwitchCount)

		// 4. 账号 300 遇到不可重试错误 → 再切换
		action = fs.HandleFailoverError(context.Background(), mock, 300, "openai", 3, switchErr)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 3, fs.SwitchCount)

		// 5. 账号 400 → 已耗尽 (SwitchCount=3 >= MaxSwitches=3)
		action = fs.HandleFailoverError(context.Background(), mock, 400, "openai", 3, switchErr)
		require.Equal(t, FailoverExhausted, action)

		// 最终状态验证
		require.Equal(t, 3, fs.SwitchCount, "耗尽时不再递增")
		require.Len(t, fs.FailedAccountIDs, 4, "4个不同账号都在失败列表中")
		require.True(t, fs.ForceCacheBilling)
		require.Len(t, mock.calls, 1, "只有账号 100 触发了 TempUnschedule")
	})

	t.Run("模拟Antigravity平台完整流程", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(2, false)

		err := newTestFailoverErr(500, false, false)

		// 第一次切换：delay = 0s
		start := time.Now()
		action := fs.HandleFailoverError(context.Background(), mock, 100, service.PlatformAntigravity, 3, err)
		elapsed := time.Since(start)
		require.Equal(t, FailoverContinue, action)
		require.Less(t, elapsed, 200*time.Millisecond, "第一次切换延迟为 0")

		// 第二次切换：delay = 1s
		start = time.Now()
		action = fs.HandleFailoverError(context.Background(), mock, 200, service.PlatformAntigravity, 3, err)
		elapsed = time.Since(start)
		require.Equal(t, FailoverContinue, action)
		require.GreaterOrEqual(t, elapsed, 800*time.Millisecond, "第二次切换延迟约 1s")

		// 第三次：耗尽（无延迟，因为在检查延迟之前就返回了）
		start = time.Now()
		action = fs.HandleFailoverError(context.Background(), mock, 300, service.PlatformAntigravity, 3, err)
		elapsed = time.Since(start)
		require.Equal(t, FailoverExhausted, action)
		require.Less(t, elapsed, 200*time.Millisecond, "耗尽时不应有延迟")
	})

	t.Run("ForceCacheBilling通过错误标志设置", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false) // hasBoundSession=false

		// 第一次：ForceCacheBilling=false
		err1 := newTestFailoverErr(500, false, false)
		fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err1)
		require.False(t, fs.ForceCacheBilling)

		// 第二次：ForceCacheBilling=true（Antigravity 粘性会话切换）
		err2 := newTestFailoverErr(500, false, true)
		fs.HandleFailoverError(context.Background(), mock, 200, "openai", 3, err2)
		require.True(t, fs.ForceCacheBilling, "错误标志应触发 ForceCacheBilling")

		// 第三次：ForceCacheBilling=false，但状态仍保持 true
		err3 := newTestFailoverErr(500, false, false)
		fs.HandleFailoverError(context.Background(), mock, 300, "openai", 3, err3)
		require.True(t, fs.ForceCacheBilling, "不应重置")
	})
}

// ---------------------------------------------------------------------------
// HandleFailoverError — 边界条件
// ---------------------------------------------------------------------------

func TestHandleFailoverError_EdgeCases(t *testing.T) {
	t.Run("StatusCode为0的错误也能正常处理", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(0, false, false)

		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 3, err)
		require.Equal(t, FailoverContinue, action)
	})

	t.Run("AccountID为0也能正常跟踪", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(500, true, false)

		action := fs.HandleFailoverError(context.Background(), mock, 0, "openai", 3, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SameAccountRetryCount[0])
	})

	t.Run("负AccountID也能正常跟踪", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(500, true, false)

		action := fs.HandleFailoverError(context.Background(), mock, -1, "openai", 3, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SameAccountRetryCount[-1])
	})

	t.Run("空平台名称不触发Antigravity延迟", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		fs.SwitchCount = 1
		err := newTestFailoverErr(500, false, false)

		start := time.Now()
		action := fs.HandleFailoverError(context.Background(), mock, 100, "", 3, err)
		elapsed := time.Since(start)

		require.Equal(t, FailoverContinue, action)
		require.Less(t, elapsed, 200*time.Millisecond, "空平台不应触发 Antigravity 延迟")
	})
}

// ---------------------------------------------------------------------------
// HandleSelectionExhausted 测试
// ---------------------------------------------------------------------------

func TestHandleSelectionExhausted(t *testing.T) {
	t.Run("无LastFailoverErr时返回Exhausted", func(t *testing.T) {
		fs := NewFailoverState(3, false)
		// LastFailoverErr 为 nil

		action := fs.HandleSelectionExhausted(context.Background(), false)
		require.Equal(t, FailoverExhausted, action)
	})

	t.Run("非503错误返回Exhausted", func(t *testing.T) {
		fs := NewFailoverState(3, false)
		fs.LastFailoverErr = newTestFailoverErr(500, false, false)

		action := fs.HandleSelectionExhausted(context.Background(), false)
		require.Equal(t, FailoverExhausted, action)
	})

	t.Run("503且未耗尽_等待后返回Continue并清除失败列表", func(t *testing.T) {
		fs := NewFailoverState(3, false)
		fs.LastFailoverErr = newTestFailoverErr(503, false, false)
		fs.FailedAccountIDs[100] = struct{}{}
		fs.SwitchCount = 1

		start := time.Now()
		action := fs.HandleSelectionExhausted(context.Background(), false)
		elapsed := time.Since(start)

		require.Equal(t, FailoverContinue, action)
		require.Empty(t, fs.FailedAccountIDs, "应清除失败账号列表")
		require.GreaterOrEqual(t, elapsed, 1500*time.Millisecond, "应等待约 2s")
		require.Less(t, elapsed, 5*time.Second)
	})

	t.Run("503但SwitchCount已超过MaxSwitches_返回Exhausted", func(t *testing.T) {
		fs := NewFailoverState(2, false)
		fs.LastFailoverErr = newTestFailoverErr(503, false, false)
		fs.SwitchCount = 3 // > MaxSwitches(2)

		start := time.Now()
		action := fs.HandleSelectionExhausted(context.Background(), false)
		elapsed := time.Since(start)

		require.Equal(t, FailoverExhausted, action)
		require.Less(t, elapsed, 100*time.Millisecond, "不应等待")
	})

	t.Run("503但context已取消_返回Canceled", func(t *testing.T) {
		fs := NewFailoverState(3, false)
		fs.LastFailoverErr = newTestFailoverErr(503, false, false)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		action := fs.HandleSelectionExhausted(ctx, false)
		elapsed := time.Since(start)

		require.Equal(t, FailoverCanceled, action)
		require.Less(t, elapsed, 100*time.Millisecond, "应立即返回")
	})

	t.Run("context已取消_非503也返回Canceled而非Exhausted", func(t *testing.T) {
		// #4257 核心场景：客户端断开后选号失败源于 context canceled，
		// 不应被当成账号耗尽转成 502。
		fs := NewFailoverState(3, false)
		fs.LastFailoverErr = newTestFailoverErr(520, false, false)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		action := fs.HandleSelectionExhausted(ctx, false)
		require.Equal(t, FailoverCanceled, action)
	})

	t.Run("context已取消_无LastFailoverErr也返回Canceled", func(t *testing.T) {
		fs := NewFailoverState(3, false)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		action := fs.HandleSelectionExhausted(ctx, false)
		require.Equal(t, FailoverCanceled, action)
	})

	t.Run("503且SwitchCount等于MaxSwitches_仍可重试", func(t *testing.T) {
		fs := NewFailoverState(2, false)
		fs.LastFailoverErr = newTestFailoverErr(503, false, false)
		fs.SwitchCount = 2 // == MaxSwitches，条件是 <=，仍可重试

		action := fs.HandleSelectionExhausted(context.Background(), false)
		require.Equal(t, FailoverContinue, action)
	})

	// TK thin-pool guard:薄池仅因 failover 排除排空时（service.ErrThinPoolAllExcluded），
	// 即使 LastFailoverErr 不是 503（典型为流内 502 或为 nil），也应退避重试该唯一账号。
	t.Run("thinPool排除_无503_仍退避重试并清除失败列表", func(t *testing.T) {
		fs := NewFailoverState(3, false)
		fs.LastFailoverErr = newTestFailoverErr(502, false, false) // 流内 SSE 包成 502
		fs.FailedAccountIDs[100] = struct{}{}
		fs.SwitchCount = 1

		start := time.Now()
		action := fs.HandleSelectionExhausted(context.Background(), true)
		elapsed := time.Since(start)

		require.Equal(t, FailoverContinue, action)
		require.Empty(t, fs.FailedAccountIDs, "薄池重试应清除排除列表，让唯一账号可再次被选中")
		require.GreaterOrEqual(t, elapsed, 1500*time.Millisecond, "应退避约 2s")
	})

	t.Run("thinPool排除_LastFailoverErr为nil_仍退避重试", func(t *testing.T) {
		fs := NewFailoverState(3, false)
		// LastFailoverErr 为 nil（首轮就排空的极端情形）
		fs.FailedAccountIDs[100] = struct{}{}
		fs.SwitchCount = 0

		action := fs.HandleSelectionExhausted(context.Background(), true)
		require.Equal(t, FailoverContinue, action)
		require.Empty(t, fs.FailedAccountIDs)
	})

	t.Run("thinPool排除_SwitchCount超过MaxSwitches_仍受上限约束返回Exhausted", func(t *testing.T) {
		fs := NewFailoverState(2, false)
		fs.LastFailoverErr = newTestFailoverErr(502, false, false)
		fs.SwitchCount = 3 // > MaxSwitches(2)

		start := time.Now()
		action := fs.HandleSelectionExhausted(context.Background(), true)
		elapsed := time.Since(start)

		require.Equal(t, FailoverExhausted, action, "薄池重试也必须受 MaxSwitches 约束，防止死循环")
		require.Less(t, elapsed, 100*time.Millisecond, "耗尽时不应等待")
	})
}

// ---------------------------------------------------------------------------
// TK: same-account retry limit is driven by the caller-supplied parameter
// (account.GetPoolModeRetryCount()), not by the old hardcoded constant.
// ---------------------------------------------------------------------------

func TestHandleFailoverError_SameAccountRetryLimit_DrivenByParameter(t *testing.T) {
	t.Run("limit=0_显式禁用retry_直接切账号", func(t *testing.T) {
		// limit=0 是运维通过 UI 显式选择"不原地重试"的语义（i18n hint:
		// "0 = 不原地重试"）。不做隐式升值，否则违反 UI 承诺。
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(503, true, false)

		action := fs.HandleFailoverError(context.Background(), mock, 100, "anthropic", 0, err)
		require.Equal(t, FailoverContinue, action)
		require.Empty(t, fs.SameAccountRetryCount, "limit=0 不应记录任何 retry")
		require.Equal(t, 1, fs.SwitchCount, "limit=0 应立即切账号")
	})

	t.Run("limit=负数_当作0处理_直接切账号", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(503, true, false)

		action := fs.HandleFailoverError(context.Background(), mock, 100, "anthropic", -5, err)
		require.Equal(t, FailoverContinue, action)
		require.Empty(t, fs.SameAccountRetryCount)
		require.Equal(t, 1, fs.SwitchCount)
	})

	t.Run("limit=1_只retry一次_第二次切账号", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(503, true, false)

		// 第一次：retry
		action := fs.HandleFailoverError(context.Background(), mock, 100, "anthropic", 1, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SameAccountRetryCount[100])
		require.Equal(t, 0, fs.SwitchCount, "retry 不应递增 SwitchCount")

		// 第二次：达到上限 → 切账号
		action = fs.HandleFailoverError(context.Background(), mock, 100, "anthropic", 1, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SameAccountRetryCount[100], "retry 计数不再递增")
		require.Equal(t, 1, fs.SwitchCount)
	})

	t.Run("limit=5_前5次retry_第6次切账号_pool_mode上限对所有平台一致", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(429, true, false)

		for i := 1; i <= 5; i++ {
			action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 5, err)
			require.Equal(t, FailoverContinue, action)
			require.Equal(t, i, fs.SameAccountRetryCount[100])
			require.Equal(t, 0, fs.SwitchCount, "iteration %d retry 不应递增 SwitchCount", i)
		}

		// 第 6 次：达到 limit → 切账号
		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 5, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount)
		require.Len(t, mock.calls, 1, "限额用尽走通用 TempUnscheduleRetryableError 路径")
	})

	t.Run("limit参数对非retryable错误无影响", func(t *testing.T) {
		mock := &mockTempUnscheduler{}
		fs := NewFailoverState(3, false)
		err := newTestFailoverErr(500, false, false) // RetryableOnSameAccount=false

		action := fs.HandleFailoverError(context.Background(), mock, 100, "openai", 10, err)
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount, "非 retryable 直接切账号，limit 不影响")
		require.Empty(t, fs.SameAccountRetryCount, "未递增 SameAccountRetryCount")
	})
}

// ---------------------------------------------------------------------------
// TK: 403 fail-fast (上游 403 + 非 anthropic JSON body)
// ---------------------------------------------------------------------------

func TestHandleFailoverError_Forbidden403FailFast(t *testing.T) {
	t.Run("403_空body_立即FailoverExhausted且不切账号不unschedule", func(t *testing.T) {
		fs := NewFailoverState(5, false)
		mock := &mockTempUnscheduler{}
		err := newTestFailoverErrWithBody(403, false, nil)

		action := fs.HandleFailoverError(context.Background(), mock, 42, "anthropic", 3, err)

		require.Equal(t, FailoverExhausted, action)
		require.Empty(t, mock.calls, "403 不应触发 TempUnscheduleRetryableError")
		require.Equal(t, 0, fs.SwitchCount, "fail-fast 路径不应递增 SwitchCount")
		require.Contains(t, fs.FailedAccountIDs, int64(42), "失败账号应加入 FailedAccountIDs")
	})

	t.Run("403_非JSON_HTML页面_FailoverExhausted", func(t *testing.T) {
		fs := NewFailoverState(5, false)
		mock := &mockTempUnscheduler{}
		body := []byte("<html><body>403 Forbidden</body></html>")
		err := newTestFailoverErrWithBody(403, false, body)

		action := fs.HandleFailoverError(context.Background(), mock, 7, "anthropic", 3, err)

		require.Equal(t, FailoverExhausted, action)
		require.Empty(t, mock.calls)
		require.Equal(t, 0, fs.SwitchCount)
	})

	t.Run("403_非anthropicJSON_FailoverExhausted", func(t *testing.T) {
		fs := NewFailoverState(5, false)
		mock := &mockTempUnscheduler{}
		body := []byte(`{"foo":"bar"}`)
		err := newTestFailoverErrWithBody(403, false, body)

		action := fs.HandleFailoverError(context.Background(), mock, 7, "anthropic", 3, err)
		require.Equal(t, FailoverExhausted, action)
		require.Equal(t, 0, fs.SwitchCount)
	})

	t.Run("403_anthropicJSON_走原failover路径切账号", func(t *testing.T) {
		fs := NewFailoverState(5, false)
		mock := &mockTempUnscheduler{}
		body := []byte(`{"type":"error","error":{"type":"forbidden","message":"x"}}`)
		err := newTestFailoverErrWithBody(403, false, body)

		action := fs.HandleFailoverError(context.Background(), mock, 7, "anthropic", 3, err)

		// 走原路径：SwitchCount 应递增到 1，进入下次循环
		require.Equal(t, FailoverContinue, action)
		require.Equal(t, 1, fs.SwitchCount, "anthropic JSON 403 应走原 failover 路径递增 SwitchCount")
	})

	t.Run("403_openaiJSON_走原failover路径切账号", func(t *testing.T) {
		fs := NewFailoverState(5, false)
		mock := &mockTempUnscheduler{}
		body := []byte(`{"error":{"message":"key revoked","type":"invalid_request_error","code":"invalid_api_key"}}`)
		err := newTestFailoverErrWithBody(403, false, body)

		action := fs.HandleFailoverError(context.Background(), mock, 7, "openai", 3, err)
		require.Equal(t, FailoverContinue, action, "openai shape 403 应走原 failover 路径")
		require.Equal(t, 1, fs.SwitchCount)
	})

	t.Run("403_geminiJSON_走原failover路径切账号", func(t *testing.T) {
		fs := NewFailoverState(5, false)
		mock := &mockTempUnscheduler{}
		body := []byte(`{"error":{"code":403,"message":"Permission denied","status":"PERMISSION_DENIED"}}`)
		err := newTestFailoverErrWithBody(403, false, body)

		action := fs.HandleFailoverError(context.Background(), mock, 7, "gemini", 3, err)
		require.Equal(t, FailoverContinue, action, "gemini shape 403 应走原 failover 路径")
		require.Equal(t, 1, fs.SwitchCount)
	})

	t.Run("403_tokenkeyInsufficientBalanceJSON_走原failover路径切账号", func(t *testing.T) {
		fs := NewFailoverState(5, false)
		mock := &mockTempUnscheduler{}
		body := []byte(`{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`)
		err := newTestFailoverErrWithBody(403, true, body)

		action := fs.HandleFailoverError(context.Background(), mock, 46, "anthropic", 1, err)
		require.Equal(t, FailoverContinue, action, "第一次应同账号 retry，不应 fail-fast")
		require.Equal(t, 0, fs.SwitchCount)

		action = fs.HandleFailoverError(context.Background(), mock, 46, "anthropic", 1, err)
		require.Equal(t, FailoverContinue, action, "retry 用尽后应 failover 到其他 stub 账号")
		require.Equal(t, 1, fs.SwitchCount)
		require.Contains(t, fs.FailedAccountIDs, int64(46))
	})

	t.Run("非403_不触发fail-fast", func(t *testing.T) {
		fs := NewFailoverState(5, false)
		mock := &mockTempUnscheduler{}
		err := newTestFailoverErrWithBody(401, false, nil)

		action := fs.HandleFailoverError(context.Background(), mock, 7, "anthropic", 3, err)

		require.Equal(t, FailoverContinue, action, "401 应走原 failover 切账号路径")
		require.Equal(t, 1, fs.SwitchCount)
	})

	t.Run("502_streamErrorWrap_不触发fail-fast", func(t *testing.T) {
		// R-001 regression: anthropic stream-error event used to be wrapped
		// as fake-403 (empty body). Now wrapped as 502 so it does not collide
		// with the 403 fail-fast judgment.
		fs := NewFailoverState(5, false)
		mock := &mockTempUnscheduler{}
		err := newTestFailoverErrWithBody(502, false, nil)

		action := fs.HandleFailoverError(context.Background(), mock, 7, "anthropic", 3, err)
		require.Equal(t, FailoverContinue, action, "stream-error wrapped 502 应走原 failover")
		require.Equal(t, 1, fs.SwitchCount)
	})

	t.Run("looksLikeStructuredErrorJSON_空body", func(t *testing.T) {
		require.False(t, looksLikeStructuredErrorJSON(nil))
		require.False(t, looksLikeStructuredErrorJSON([]byte{}))
	})

	t.Run("looksLikeStructuredErrorJSON_非JSON", func(t *testing.T) {
		require.False(t, looksLikeStructuredErrorJSON([]byte("not json at all")))
		require.False(t, looksLikeStructuredErrorJSON([]byte("<html></html>")))
	})

	t.Run("looksLikeStructuredErrorJSON_合法JSON但无error字段", func(t *testing.T) {
		require.False(t, looksLikeStructuredErrorJSON([]byte(`{"foo":"bar"}`)))
		require.False(t, looksLikeStructuredErrorJSON([]byte(`{"type":"error","error":"string"}`)),
			"error 字段必须是 object")
	})

	t.Run("looksLikeStructuredErrorJSON_anthropic_openai_gemini", func(t *testing.T) {
		require.True(t, looksLikeStructuredErrorJSON([]byte(`{"type":"error","error":{"type":"forbidden","message":"x"}}`)),
			"anthropic shape")
		require.True(t, looksLikeStructuredErrorJSON([]byte(`{"error":{"message":"x","type":"y","code":"z"}}`)),
			"openai shape")
		require.True(t, looksLikeStructuredErrorJSON([]byte(`{"error":{"code":403,"message":"x","status":"y"}}`)),
			"gemini shape")
		require.True(t, looksLikeStructuredErrorJSON([]byte(`{"error":{}}`)),
			"任何有 error object 的 JSON 都视为结构化错误")
		require.True(t, looksLikeStructuredErrorJSON([]byte(`{"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`)),
			"TokenKey middleware shape")
	})
}

// ---------------------------------------------------------------------------
// failoverClientGone 测试
// ---------------------------------------------------------------------------

func TestFailoverClientGone(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("活跃请求返回false", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

		require.False(t, failoverClientGone(c))
		require.Equal(t, http.StatusOK, c.Writer.Status(), "不应改动状态码")
	})

	t.Run("客户端已断开_返回true并标记499", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)

		require.True(t, failoverClientGone(c))
		require.Equal(t, statusClientClosedRequest, c.Writer.Status())
	})

	t.Run("响应已提交_不改状态码", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		c.String(http.StatusOK, "partial")

		require.True(t, failoverClientGone(c))
		require.Equal(t, http.StatusOK, c.Writer.Status(), "已提交的状态码不应被覆盖")
	})

	t.Run("nil安全", func(t *testing.T) {
		require.False(t, failoverClientGone(nil))
	})
}

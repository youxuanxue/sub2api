//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 复用 account_incident_notifier_tk_test.go 的 fakes:
// blockingFeishuDoer / fakeIncidentConfigProvider / enabledFeishuConfig / newTestNotifier / testAccount。

// G4(#600)模型类 cooldown 的 reason 必须被正确分类,而非落兜底"账号临时冷却"。
func TestClassifyIncident_429ModelClass(t *testing.T) {
	t.Parallel()
	got := classifyIncident("429_model_class", time.Now().Add(time.Hour), IncidentKindUnknown)
	require.True(t, got.alert)
	require.Equal(t, IncidentKindTemporaryCooldown, got.kind)
	require.Equal(t, "429_model_class", got.reasonClass)
	require.Contains(t, got.kindZh, "模型类")
	require.Contains(t, got.kindZh, "账号其它模型仍可调度")
}

// 永久失效告警后,真实清除事件 → 即时绿色恢复卡;active 台账清空。
func TestNotifyAccountRecovered_AfterPermanent_SendsGreen(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 4)}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))

	n.NotifyAccountIncident(testAccount(42, "cc-main", "anthropic"), time.Time{}, "auth_error", IncidentKindUnknown)
	waitSend(t, doer, "permanent red")

	n.NotifyAccountRecovered(42)
	waitSend(t, doer, "recovery green")

	require.Equal(t, 2, doer.callCount())
	body := doer.lastBody()
	require.Contains(t, body, "账号已恢复调度")
	require.Contains(t, body, "cc-main")

	n.mu.Lock()
	require.Empty(t, n.active[42], "active 台账应在恢复后清空")
	n.mu.Unlock()
}

// 临时冷却(进聚合,不即时发)后,真实清除事件 → 即时绿色恢复卡。
func TestNotifyAccountRecovered_AfterTemporary_SendsGreen(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 4)}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))

	n.NotifyAccountIncident(testAccount(7, "edge-opus", "anthropic"), time.Unix(1700003600, 0), "429_model_class", IncidentKindUnknown)
	require.Equal(t, 0, doer.callCount(), "临时冷却不即时发")

	n.NotifyAccountRecovered(7)
	waitSend(t, doer, "recovery green")
	require.Equal(t, 1, doer.callCount())
	require.Contains(t, doer.lastBody(), "账号已恢复调度")

	n.mu.Lock()
	require.Empty(t, n.active[7])
	n.mu.Unlock()
}

// 从未告警过的账号 → NotifyAccountRecovered 为 no-op(不发恢复卡)。
func TestNotifyAccountRecovered_UnknownAccount_NoSend(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 1)}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))

	n.NotifyAccountRecovered(999)
	select {
	case <-doer.done:
		t.Fatal("未告警账号不应发恢复卡")
	case <-time.After(150 * time.Millisecond):
	}
	require.Equal(t, 0, doer.callCount())
}

// 短窗内重复清除 → 只发一张恢复卡(去重),但台账每次都清。
func TestNotifyAccountRecovered_DedupeWithinWindow(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 4)}
	now := time.Unix(1700000000, 0)
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, now)

	// 第一轮:告警 → 恢复(发绿卡)。
	n.NotifyAccountIncident(testAccount(42, "cc-main", "anthropic"), now.Add(time.Minute), "429", IncidentKindUnknown)
	n.NotifyAccountRecovered(42)
	waitSend(t, doer, "first recovery")

	// 第二轮:同账号再次告警 → 同窗内再清除 → 去重,不重发。
	n.NotifyAccountIncident(testAccount(42, "cc-main", "anthropic"), now.Add(time.Minute), "429", IncidentKindUnknown)
	n.NotifyAccountRecovered(42)
	select {
	case <-doer.done:
		t.Fatal("短窗内重复恢复应被去重")
	case <-time.After(150 * time.Millisecond):
	}
	require.Equal(t, 1, doer.callCount())

	n.mu.Lock()
	require.Empty(t, n.active[42], "台账仍应被清(即使绿卡去重)")
	n.mu.Unlock()
}

// 纯定时器到期自愈(无清除事件):pruneStaleActive 静默删台账,不发恢复卡。
func TestPruneStaleActive_SilentNoSend(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 1)}
	now := time.Unix(1700000000, 0)
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, now)

	// until 已过期超过宽限(1h):until = now-2h。
	n.NotifyAccountIncident(testAccount(7, "edge-opus", "anthropic"), now.Add(-2*time.Hour), "429_model_class", IncidentKindUnknown)
	n.mu.Lock()
	require.Len(t, n.active[7], 1)
	n.mu.Unlock()

	n.pruneStaleActive()

	n.mu.Lock()
	require.Empty(t, n.active[7], "陈旧临时条目应被静默修剪")
	n.mu.Unlock()
	select {
	case <-doer.done:
		t.Fatal("静默修剪不得发恢复卡")
	case <-time.After(150 * time.Millisecond):
	}
	require.Equal(t, 0, doer.callCount())
}

func waitSend(t *testing.T, doer *blockingFeishuDoer, what string) {
	t.Helper()
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not send within 2s", what)
	}
}

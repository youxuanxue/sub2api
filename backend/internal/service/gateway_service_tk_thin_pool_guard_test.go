package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// newThinPoolSettingService builds a SettingService over the shared in-package
// adminComplianceRepoStub (admin_compliance_test.go) seeded with the given keys.
func newThinPoolSettingService(values map[string]string) *SettingService {
	return NewSettingService(&adminComplianceRepoStub{values: values}, &config.Config{})
}

func TestIsThinPoolTransientRetryEnabled(t *testing.T) {
	t.Run("默认开启_无键", func(t *testing.T) {
		svc := newThinPoolSettingService(nil)
		require.True(t, svc.IsThinPoolTransientRetryEnabled(context.Background()))
	})
	t.Run("显式false_关闭", func(t *testing.T) {
		svc := newThinPoolSettingService(map[string]string{SettingKeyThinPoolTransientRetryEnabled: "false"})
		require.False(t, svc.IsThinPoolTransientRetryEnabled(context.Background()))
	})
	t.Run("显式true_开启", func(t *testing.T) {
		svc := newThinPoolSettingService(map[string]string{SettingKeyThinPoolTransientRetryEnabled: "true"})
		require.True(t, svc.IsThinPoolTransientRetryEnabled(context.Background()))
	})
	t.Run("空值_回退默认开启", func(t *testing.T) {
		svc := newThinPoolSettingService(map[string]string{SettingKeyThinPoolTransientRetryEnabled: "  "})
		require.True(t, svc.IsThinPoolTransientRetryEnabled(context.Background()))
	})
	t.Run("乱码_fail_open默认开启", func(t *testing.T) {
		svc := newThinPoolSettingService(map[string]string{SettingKeyThinPoolTransientRetryEnabled: "notabool"})
		require.True(t, svc.IsThinPoolTransientRetryEnabled(context.Background()))
	})
}

func TestTkThinPoolAllExcluded(t *testing.T) {
	ctx := context.Background()

	t.Run("单账号_仅排除_命中", func(t *testing.T) {
		gs := &GatewayService{settingService: newThinPoolSettingService(nil)}
		require.True(t, gs.tkThinPoolAllExcluded(ctx, 1, 1, 0))
	})
	t.Run("单账号_有其他过滤_不命中", func(t *testing.T) {
		// 例如账号被冷却/不可调度（filtered_unschedulable>0）→ 尊重真冷却，不绕过。
		gs := &GatewayService{settingService: newThinPoolSettingService(nil)}
		require.False(t, gs.tkThinPoolAllExcluded(ctx, 1, 1, 1))
	})
	t.Run("无排除_不命中", func(t *testing.T) {
		gs := &GatewayService{settingService: newThinPoolSettingService(nil)}
		require.False(t, gs.tkThinPoolAllExcluded(ctx, 1, 0, 0))
	})
	t.Run("N=2_全部失败排除_命中", func(t *testing.T) {
		// 真正的现场形状（cc-us2/cc-us4，prod 12h 893×）：2 个号都被本请求自己的
		// failover 排空，其它过滤计数全 0 → 应救援（旧实现因 N>1 天花板漏掉）。
		gs := &GatewayService{settingService: newThinPoolSettingService(nil)}
		require.True(t, gs.tkThinPoolAllExcluded(ctx, 2, 2, 0))
	})
	t.Run("N=5_全部失败排除_命中", func(t *testing.T) {
		gs := &GatewayService{settingService: newThinPoolSettingService(nil)}
		require.True(t, gs.tkThinPoolAllExcluded(ctx, 5, 5, 0))
	})
	t.Run("N=2_有真冷却_不命中_amplifier守卫", func(t *testing.T) {
		// 一个被 failover 排除、另一个被真冷却/配额摘掉（otherFiltersSum>0）→ 守卫
		// 必须不触发，尊重真限流，绝不绕过。这是放宽天花板后的关键安全性。
		gs := &GatewayService{settingService: newThinPoolSettingService(nil)}
		require.False(t, gs.tkThinPoolAllExcluded(ctx, 2, 1, 1))
	})
	t.Run("N=3_混入其他过滤_不命中", func(t *testing.T) {
		gs := &GatewayService{settingService: newThinPoolSettingService(nil)}
		require.False(t, gs.tkThinPoolAllExcluded(ctx, 3, 2, 1))
	})
	t.Run("总数0_不命中", func(t *testing.T) {
		gs := &GatewayService{settingService: newThinPoolSettingService(nil)}
		require.False(t, gs.tkThinPoolAllExcluded(ctx, 0, 0, 0))
	})
	t.Run("kill_switch关闭_不命中", func(t *testing.T) {
		gs := &GatewayService{settingService: newThinPoolSettingService(map[string]string{SettingKeyThinPoolTransientRetryEnabled: "false"})}
		require.False(t, gs.tkThinPoolAllExcluded(ctx, 1, 1, 0))
	})
	t.Run("settingService为nil_不命中", func(t *testing.T) {
		gs := &GatewayService{}
		require.False(t, gs.tkThinPoolAllExcluded(ctx, 1, 1, 0))
	})
}

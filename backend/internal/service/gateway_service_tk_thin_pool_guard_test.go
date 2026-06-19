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

func TestThinPoolMaxAccounts(t *testing.T) {
	t.Run("默认1", func(t *testing.T) {
		svc := newThinPoolSettingService(nil)
		require.Equal(t, 1, svc.ThinPoolMaxAccounts(context.Background()))
	})
	t.Run("覆写为3", func(t *testing.T) {
		svc := newThinPoolSettingService(map[string]string{SettingKeyThinPoolMaxAccounts: "3"})
		require.Equal(t, 3, svc.ThinPoolMaxAccounts(context.Background()))
	})
	t.Run("非法值回退默认", func(t *testing.T) {
		svc := newThinPoolSettingService(map[string]string{SettingKeyThinPoolMaxAccounts: "abc"})
		require.Equal(t, 1, svc.ThinPoolMaxAccounts(context.Background()))
	})
	t.Run("小于1回退默认", func(t *testing.T) {
		svc := newThinPoolSettingService(map[string]string{SettingKeyThinPoolMaxAccounts: "0"})
		require.Equal(t, 1, svc.ThinPoolMaxAccounts(context.Background()))
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
	t.Run("池非薄_超过阈值_不命中", func(t *testing.T) {
		gs := &GatewayService{settingService: newThinPoolSettingService(nil)}
		require.False(t, gs.tkThinPoolAllExcluded(ctx, 2, 1, 0))
	})
	t.Run("总数0_不命中", func(t *testing.T) {
		gs := &GatewayService{settingService: newThinPoolSettingService(nil)}
		require.False(t, gs.tkThinPoolAllExcluded(ctx, 0, 0, 0))
	})
	t.Run("阈值覆写为2_双账号仅排除_命中", func(t *testing.T) {
		gs := &GatewayService{settingService: newThinPoolSettingService(map[string]string{SettingKeyThinPoolMaxAccounts: "2"})}
		require.True(t, gs.tkThinPoolAllExcluded(ctx, 2, 1, 0))
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

//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests for docs/bugs/2026-04-23-newapi-fifth-platform-audit.md P0-1 + P1-2.
//
// P0-1: defaultOpenAIAccountScheduler 之前完全跳过 channel pricing /
//       upstream model restriction 检查，与 SelectAccountWithLoadAwareness 入口
//       行为漂移。本测试族验证修复后两个入口语义一致。
//
// P1-2: selectByLoadBalance 错误信息按 GroupPlatform 区分（OpenAI / newapi 等），
//       不再硬写 "no available OpenAI accounts"。

// newSchedFixtureWithChannel 复用 newAPISchedFixture（同包测试 fixture），
// 在其之上挂一个真实的 ChannelService（带 mock repo），用于触发 channel
// pricing / restriction 路径。channelService==nil 时
// checkChannelPricingRestriction / needsUpstreamChannelRestrictionCheck 都直接
// return false 短路，无法测试。
//
// OPC 原则：不复制 newAPISchedFixture 的 30+ 行 setup，只在它之上加 1 行
// channelService 注入；任何对基础 fixture 的修改都自动传导到这里。
func newSchedFixtureWithChannel(
	t *testing.T,
	groupID int64,
	groupPlatform string,
	pool []*Account,
	channel Channel,
) (*OpenAIGatewayService, *defaultOpenAIAccountScheduler) {
	t.Helper()
	resetOpenAIAdvancedSchedulerSettingCacheForTest()

	svc, sched := newAPISchedFixture(t, groupID, groupPlatform, pool)
	svc.channelService = newTestChannelService(
		makeStandardRepo(channel, map[int64]string{groupID: groupPlatform}),
	)
	return svc, sched
}

// TestP01_Scheduler_ChannelPricingRestriction_Blocks 验证调度入口前置拒绝：
// 请求的模型不在渠道定价表内时，必须返回 ErrNoAvailableAccounts，绝不进入
// 选号流程。与 SelectAccountWithLoadAwareness 入口的 checkChannelPricingRestriction
// 行为对齐。
func TestP01_Scheduler_ChannelPricingRestriction_Blocks(t *testing.T) {
	ctx := context.Background()
	groupID := int64(91001)
	pool := []*Account{newAPIAccount(91101, 7)}
	channel := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{groupID},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceRequested,
		ModelPricing: []ChannelModelPricing{
			// 只允许 gpt-5.4，不允许 gpt-restricted
			{Platform: PlatformNewAPI, Models: []string{"gpt-5.4"}},
		},
	}
	svc, _ := newSchedFixtureWithChannel(t, groupID, PlatformNewAPI, pool, channel)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-restricted", nil, OpenAIUpstreamTransportAny, false)
	require.Error(t, err, "P0-1: scheduler must block requests for models outside channel pricing whitelist")
	require.True(t, selection == nil || selection.Account == nil, "no account may be selected when model is restricted")
	require.Contains(t, strings.ToLower(err.Error()), "channel pricing restriction",
		"error must clearly indicate the restriction reason for operator triage")
}

// TestP01_Scheduler_ChannelPricingRestriction_AllowsListedModel 回归保护：
// 请求模型在白名单内时正常选号，不被新加的检查误杀。
func TestP01_Scheduler_ChannelPricingRestriction_AllowsListedModel(t *testing.T) {
	ctx := context.Background()
	groupID := int64(91002)
	pool := []*Account{newAPIAccount(91201, 7)}
	channel := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{groupID},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceRequested,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformNewAPI, Models: []string{"gpt-5.4"}},
		},
	}
	svc, _ := newSchedFixtureWithChannel(t, groupID, PlatformNewAPI, pool, channel)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.4", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err, "whitelisted model must be allowed through the new restriction gate")
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(91201), selection.Account.ID)
}

// TestP01_Scheduler_NoChannelService_NoRegression 当 channelService==nil（旧测试
// 的默认 fixture），新加的检查必须短路返回 false，行为与之前完全一致。
func TestP01_Scheduler_NoChannelService_NoRegression(t *testing.T) {
	ctx := context.Background()
	groupID := int64(91003)
	pool := []*Account{newAPIAccount(91301, 7)}
	svc, _ := newAPISchedFixture(t, groupID, PlatformNewAPI, pool)
	require.Nil(t, svc.channelService, "fixture sanity: channelService must be nil here")

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "any-model", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(91301), selection.Account.ID,
		"channelService==nil must short-circuit restriction checks (regression guard)")
}

// TestP12_Scheduler_EmptyPool_ErrorIsPlatformAware 验证 P1-2：错误信息按
// GroupPlatform 区分，不再硬写 "OpenAI"。
func TestP12_Scheduler_EmptyPool_ErrorIsPlatformAware(t *testing.T) {
	ctx := context.Background()

	// newapi 平台 + 空 pool（仅有跨平台噪声账号会被 IsOpenAICompatPoolMember 过滤掉）
	groupID := int64(91004)
	pool := []*Account{openAIAccount(91401, 0)}
	svc, _ := newAPISchedFixture(t, groupID, PlatformNewAPI, pool)

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.Error(t, err)
	require.True(t, selection == nil || selection.Account == nil)
	msg := strings.ToLower(err.Error())
	require.Contains(t, msg, "newapi",
		"P1-2: empty newapi pool error must mention 'newapi' platform, not 'OpenAI' (got %q)", err.Error())

	// openai 平台 + 空 pool —— 错误必须仍包含 "openai"，保证旧告警/grep 不挂
	groupIDOA := int64(91005)
	poolOA := []*Account{newAPIAccount(91501, 7)}
	svcOA, _ := newAPISchedFixture(t, groupIDOA, PlatformOpenAI, poolOA)

	_, _, errOA := svcOA.SelectAccountWithScheduler(ctx, &groupIDOA, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.Error(t, errOA)
	require.Contains(t, strings.ToLower(errOA.Error()), "openai",
		"P1-2: empty openai pool error must still mention 'openai' (got %q)", errOA.Error())
}

// TestP12_Helper_FallbackToOpenAI 单元覆盖 helper 在空 platform 时的回退行为，
// 防止未来重构把 fallback 改丢。
func TestP12_Helper_FallbackToOpenAI(t *testing.T) {
	require.Equal(t, PlatformOpenAI, openAICompatErrorPlatformLabel(""),
		"empty groupPlatform must fall back to 'openai' for backward-compat log greps")
	require.Equal(t, PlatformNewAPI, openAICompatErrorPlatformLabel(PlatformNewAPI))
	require.Equal(t, PlatformOpenAI, openAICompatErrorPlatformLabel(PlatformOpenAI))
}

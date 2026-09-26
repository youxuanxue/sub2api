package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAI403CounterResetStub struct {
	resetCalls []int64
}

func (s *openAI403CounterResetStub) IncrementOpenAI403Count(context.Context, int64, int) (int64, error) {
	return 0, nil
}

func (s *openAI403CounterResetStub) ResetOpenAI403Count(_ context.Context, accountID int64) error {
	s.resetCalls = append(s.resetCalls, accountID)
	return nil
}

func TestOpenAIGatewayServiceRecordUsage_ResetsOpenAI403CounterForZeroUsage(t *testing.T) {
	counter := &openAI403CounterResetStub{}
	rateLimitSvc := NewRateLimitService(nil, nil, nil, nil, nil)
	rateLimitSvc.SetOpenAI403CounterCache(counter)

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo, nil)
	svc.rateLimitService = rateLimitSvc

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_zero_usage_reset_403",
			Model:     "gpt-5.1",
		},
		APIKey:  &APIKey{ID: 1001, Group: &Group{RateMultiplier: 1}},
		User:    &User{ID: 2001},
		Account: &Account{ID: 777, Platform: PlatformOpenAI},
	})

	require.NoError(t, err)
	require.Equal(t, []int64{777}, counter.resetCalls)
	require.Equal(t, 1, usageRepo.calls)
}

// CN 供应商与 OpenCodeGo 账号同样会被 handle403 喂进累计 403 阶梯
// （UsesOpenAI403Ladder），因此成功响应必须同样清零。修复前这里卡了
// Platform == PlatformOpenAI，导致这些账号的计数只增不减：在 180 分钟窗口里
// 零散碰到 3 次不相关的 403，中间夹着成千上万次成功请求，第 3 次就把一个完全
// 健康、正在正常服务的账号永久禁用。不依赖并发，单线程即可触发。
func TestOpenAIGatewayServiceRecordUsage_Resets403CounterForEveryLadderPlatform(t *testing.T) {
	platforms := []string{
		PlatformOpenAI,
		PlatformOpenCodeGo,
		PlatformKimi,
		PlatformZhipu,
		PlatformDeepseek,
		PlatformMiniMax,
	}

	for _, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			require.True(t, UsesOpenAI403Ladder(platform),
				"该 platform 必须在阶梯覆盖集内，否则本用例的前提失效")

			counter := &openAI403CounterResetStub{}
			rateLimitSvc := NewRateLimitService(nil, nil, nil, nil, nil)
			rateLimitSvc.SetOpenAI403CounterCache(counter)

			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(
				usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{},
				&openAIRecordUsageSubRepoStub{}, nil,
			)
			svc.rateLimitService = rateLimitSvc

			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result:  &OpenAIForwardResult{RequestID: "resp_" + platform, Model: "gpt-5.1"},
				APIKey:  &APIKey{ID: 1001, Group: &Group{RateMultiplier: 1}},
				User:    &User{ID: 2001},
				Account: &Account{ID: 778, Platform: platform},
			})

			require.NoError(t, err)
			require.Equal(t, []int64{778}, counter.resetCalls,
				"走累计 403 阶梯的 platform 必须在成功响应时清零计数")
		})
	}
}

// 不走该阶梯的 platform 不应消耗这个清零路径：阶梯覆盖集是单一事实来源，
// 递增侧与清零侧必须引用同一个判断，不能各自手列 platform。
func TestUsesOpenAI403Ladder_覆盖集与递增侧一致(t *testing.T) {
	for _, platform := range []string{
		PlatformOpenAI, PlatformOpenCodeGo,
		PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax,
	} {
		require.True(t, UsesOpenAI403Ladder(platform), platform)
	}
	for _, platform := range []string{
		PlatformAnthropic, PlatformAntigravity, PlatformGemini,
	} {
		require.False(t, UsesOpenAI403Ladder(platform), platform)
	}
}

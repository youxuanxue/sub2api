//go:build unit

package service

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type stubSignupBonusIPCounter struct {
	mu     sync.Mutex
	counts map[string]int64
}

func (s *stubSignupBonusIPCounter) IncrDaily(_ context.Context, ip string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = make(map[string]int64)
	}
	s.counts[ip]++
	return s.counts[ip], nil
}

func TestApplySignupBonusUSD_WithholdsAfterIPDailyLimit(t *testing.T) {
	counter := &stubSignupBonusIPCounter{}
	repo := &userRepoStub{nextID: 1}
	svc := newAuthService(repo, map[string]string{
		SettingKeyRegistrationEnabled:     "true",
		SettingKeySignupBonusEnabled:      "true",
		SettingKeySignupBonusBalance:      "1.00",
		SettingKeySignupBonusIPDailyLimit: "2",
	}, nil, nil)
	svc.SetSignupBonusIPCounter(counter)

	ip := "203.0.113.50"
	for i := 0; i < 2; i++ {
		total, bonus := svc.applySignupBonusUSD(WithSignupClientIP(context.Background(), ip), 0)
		require.InDelta(t, 1.0, total, 0.0001, "grant #%d", i+1)
		require.InDelta(t, 1.0, bonus, 0.0001, "grant #%d", i+1)
	}
	total, bonus := svc.applySignupBonusUSD(WithSignupClientIP(context.Background(), ip), 0)
	require.InDelta(t, 0, total, 0.0001, "3rd grant from same IP must withhold")
	require.InDelta(t, 0, bonus, 0.0001)

	total, bonus = svc.applySignupBonusUSD(WithSignupClientIP(context.Background(), "198.51.100.9"), 0)
	require.InDelta(t, 1.0, total, 0.0001)
	require.InDelta(t, 1.0, bonus, 0.0001)
}

func TestApplySignupBonusUSD_LimitZeroDisablesWithhold(t *testing.T) {
	counter := &stubSignupBonusIPCounter{}
	svc := newAuthService(&userRepoStub{nextID: 1}, map[string]string{
		SettingKeySignupBonusEnabled:      "true",
		SettingKeySignupBonusBalance:      "1.00",
		SettingKeySignupBonusIPDailyLimit: "0",
	}, nil, nil)
	svc.SetSignupBonusIPCounter(counter)

	ip := "203.0.113.51"
	for i := 0; i < 5; i++ {
		_, bonus := svc.applySignupBonusUSD(WithSignupClientIP(context.Background(), ip), 0)
		require.InDelta(t, 1.0, bonus, 0.0001, "iteration %d", i)
	}
	require.Empty(t, counter.counts, "limit=0 must not touch the counter")
}

func TestEvaluateTrialUnpaidMedia(t *testing.T) {
	settings := newAuthService(&userRepoStub{}, map[string]string{
		SettingKeyTrialUnpaidMediaBlocked:    "true",
		SettingKeyTrialUnpaidMediaMaxBalance: "2.00",
	}, nil, nil).settingService

	trial := &User{Role: RoleUser, Balance: 1.0, TotalRecharged: 0}
	require.True(t, EvaluateTrialUnpaidMedia(context.Background(), trial, ShapeOpenAIImages, http.MethodPost, settings).Blocked)
	require.False(t, EvaluateTrialUnpaidMedia(context.Background(), trial, ShapeOpenAIVideo, http.MethodGet, settings).Blocked,
		"video status poll must stay open")
	require.False(t, EvaluateTrialUnpaidMedia(context.Background(), trial, ShapeOpenAIChat, http.MethodPost, settings).Blocked,
		"text chat must stay open")

	paid := &User{Role: RoleUser, Balance: 0.1, TotalRecharged: 5}
	require.False(t, EvaluateTrialUnpaidMedia(context.Background(), paid, ShapeOpenAIImages, http.MethodPost, settings).Blocked)

	opsFat := &User{Role: RoleUser, Balance: 5000, TotalRecharged: 0}
	require.False(t, EvaluateTrialUnpaidMedia(context.Background(), opsFat, ShapeOpenAIImages, http.MethodPost, settings).Blocked,
		"admin-granted fat wallets must not be treated as trial")

	admin := &User{Role: RoleAdmin, Balance: 0, TotalRecharged: 0}
	require.False(t, EvaluateTrialUnpaidMedia(context.Background(), admin, ShapeOpenAIImages, http.MethodPost, settings).Blocked)

	disabled := newAuthService(&userRepoStub{}, map[string]string{
		SettingKeyTrialUnpaidMediaBlocked: "false",
	}, nil, nil).settingService
	require.False(t, EvaluateTrialUnpaidMedia(context.Background(), trial, ShapeOpenAIImages, http.MethodPost, disabled).Blocked)
}

package handler

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// maxProfitVetoAttempts 单次请求内允许的分组利润门终检否决次数上限。
// 利润否决不产生上游请求，因此不会推进 SwitchCount；没有独立上限的话，
// 「选号 → 终检否决 → 重选」在候选池与账号快照短暂不一致时可以空转很久。
// 取值与 maxAccountSwitches 默认值一致：混合定价的大分组仍有充分重选机会，
// 同时把整池越线时的无谓选号开销限制在常数级。
const maxProfitVetoAttempts = 10

// profitVetoExhaustedMessage 是利润否决次数耗尽时返回给客户端的文案。
// 语义上等同于「无可用账号」：候选账号都不满足分组的利润约束。
const profitVetoExhaustedMessage = "No available accounts: all candidates rejected by group profit control"

// RecordProfitVeto 记录一次分组利润门终检否决：把账号加入排除列表（同时登记到
// 利润否决集，使其不被 503 退避分支清掉）并递增否决计数。
//
// 返回 FailoverContinue 表示调用方可以继续重选下一个账号；返回 FailoverExhausted
// 表示本次请求的利润否决次数已达上限，调用方应按「无可用账号」终止，
// 不得继续 continue。
func (s *FailoverState) RecordProfitVeto(accountID int64) FailoverAction {
	s.FailedAccountIDs[accountID] = struct{}{}
	if s.profitVetoedAccountIDs == nil {
		s.profitVetoedAccountIDs = make(map[int64]struct{})
	}
	s.profitVetoedAccountIDs[accountID] = struct{}{}
	s.profitVetoCount++
	if s.profitVetoCount >= maxProfitVetoAttempts {
		return FailoverExhausted
	}
	return FailoverContinue
}

// ProfitVetoCount 返回本次请求累计的利润否决次数（供日志使用）。
func (s *FailoverState) ProfitVetoCount() int { return s.profitVetoCount }

// allExclusionsAreProfitVetoed 判断排除列表是否已全部由利润门否决贡献。
// 此时清空 FailedAccountIDs 会被原样恢复，退避重试不会带来任何新候选。
func (s *FailoverState) allExclusionsAreProfitVetoed() bool {
	if len(s.profitVetoedAccountIDs) == 0 || len(s.FailedAccountIDs) == 0 {
		return false
	}
	for id := range s.FailedAccountIDs {
		if _, ok := s.profitVetoedAccountIDs[id]; !ok {
			return false
		}
	}
	return true
}

// maybeExhaustByProfitVeto 在选号耗尽退避分支上拦截「全利润否决」活锁。
// 返回 true 时调用方应立即 FailoverExhausted。
func (s *FailoverState) maybeExhaustByProfitVeto(ctx context.Context) bool {
	if !s.allExclusionsAreProfitVetoed() {
		return false
	}
	logger.FromContext(ctx).Warn("gateway.failover_selection_exhausted_by_profit_veto",
		zap.Int("profit_veto_count", s.profitVetoCount),
		zap.Int("excluded_accounts", len(s.FailedAccountIDs)),
	)
	return true
}

// restoreProfitVetoExclusions 把利润门否决的账号放回排除集。
// 判定依据（冻结的下游倍率）在同一请求内不变，放它们回池只会被再次否决。
func (s *FailoverState) restoreProfitVetoExclusions() {
	for id := range s.profitVetoedAccountIDs {
		s.FailedAccountIDs[id] = struct{}{}
	}
}

// TokenKey middleware shape: {"code":"INSUFFICIENT_BALANCE","message":"..."}.
func looksLikeStructuredErrorJSON(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if !json.Valid(body) {
		return false
	}
	if gjson.GetBytes(body, "error").IsObject() {
		return true
	}
	code := strings.TrimSpace(gjson.GetBytes(body, "code").String())
	msg := strings.TrimSpace(gjson.GetBytes(body, "message").String())
	return code != "" && msg != ""
}

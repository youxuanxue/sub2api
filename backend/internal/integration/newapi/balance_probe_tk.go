package newapi

// TK: 上游渠道账号「余额探测」——给后台 upstream_balance_sentinel 哨兵复用。
//
// 背景：newapi 第五平台的上游渠道账号余额耗尽时，bridge 把 402/429 吞码掩成 502
// 无限锤上游（见 2026-06-11 DeepSeek 余额归零事故）。被动 402 告警只能事后报，要
// 提前预防只能主动拉余额。不是每个渠道都有公开余额 API——只有 DeepSeek 提供
// GET /user/balance，故这里按 channel_type 派发，v1 仅注册 DeepSeek(43)。加新渠道 =
// 注册一个探测函数，哨兵主循环零改动。
//
// 本包（integration/newapi）位于 service 之下，禁止 import service（会成环）。因此
// HTTP 出站走一个本地最小 doer 接口，service.HTTPUpstream 结构上即满足它。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// HTTPDoer 是余额探测所需的最小上游 HTTP 出站面。签名与 service.HTTPUpstream.Do
// 对齐，故注入 service.HTTPUpstream 即满足（无需本包 import service）。
type HTTPDoer interface {
	Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error)
}

// BalanceResult 是一次余额探测的归一化结果。AvailableCNY 是可用余额（人民币）；
// IsAvailable 是上游自报的「账号当前是否可用」硬信号（DeepSeek 余额归零时为 false）。
type BalanceResult struct {
	AvailableCNY float64
	IsAvailable  bool
}

// BalanceProbeFn 探测单个上游渠道账号的余额。
type BalanceProbeFn func(ctx context.Context, doer HTTPDoer, baseURL, apiKey, proxyURL string, accountID int64, accountConcurrency int) (BalanceResult, error)

// balanceProbeRegistry 是 channel_type → 探测函数的派发表。
//
// 只收「有 per-api-key 余额端点」的渠道——这是准入硬条件，不是当前实现的偷懒：
//   - DeepSeek：`GET /user/balance`（Bearer api_key），可直接用账号已存的 key 探。✅
//   - 火山方舟 (45) / 通义千问·Ali (17)：余额是**云账号级**，只能走各自的 Billing/BSS
//     OpenAPI（火山 V4 签名 / 阿里 RPC 签名），凭证是 AK/SK 而非渠道 api_key，且粒度对
//     不到单渠道账号——结构上无法低成本复用本探测面。它们的余额耗尽继续靠反应式 #724
//     402 透传 + 账号惩罚兜底；真要主动监控得另起「云账号级余额」ops job，别塞进这里。
var balanceProbeRegistry = map[int]BalanceProbeFn{
	newapiconstant.ChannelTypeDeepSeek: ProbeDeepSeekBalance,
}

// BalanceProbeFor 返回某 channel_type 的余额探测函数（若该渠道支持公开余额查询）。
func BalanceProbeFor(channelType int) (BalanceProbeFn, bool) {
	fn, ok := balanceProbeRegistry[channelType]
	return fn, ok
}

const deepSeekDefaultBaseURL = "https://api.deepseek.com"

// deepSeekBalanceResponse 映射 GET /user/balance 的响应：
//
//	{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"298.02",...}]}
//
// total_balance 是字符串数值，需 ParseFloat。
type deepSeekBalanceResponse struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency     string `json:"currency"`
		TotalBalance string `json:"total_balance"`
	} `json:"balance_infos"`
}

// ProbeDeepSeekBalance 拉 DeepSeek 账号的 CNY 可用余额。
func ProbeDeepSeekBalance(ctx context.Context, doer HTTPDoer, baseURL, apiKey, proxyURL string, accountID int64, accountConcurrency int) (BalanceResult, error) {
	if doer == nil {
		return BalanceResult{}, fmt.Errorf("deepseek balance: nil http doer")
	}
	if strings.TrimSpace(apiKey) == "" {
		return BalanceResult{}, fmt.Errorf("deepseek balance: empty api_key")
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = deepSeekDefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user/balance", nil)
	if err != nil {
		return BalanceResult{}, fmt.Errorf("deepseek balance: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	req.Header.Set("Accept", "application/json")

	resp, err := doer.Do(req, proxyURL, accountID, accountConcurrency)
	if err != nil {
		return BalanceResult{}, fmt.Errorf("deepseek balance: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return BalanceResult{}, fmt.Errorf("deepseek balance: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// 401/403 = 凭证失效；其它非 200 = 上游抖动。两者都不是「低余额」，交由
		// 哨兵记心跳错误、不发预警（避免误报）。
		return BalanceResult{}, fmt.Errorf("deepseek balance: upstream status %d: %s", resp.StatusCode, truncateForErr(body))
	}

	return parseDeepSeekBalance(body)
}

// parseDeepSeekBalance 独立出来便于单测（确定性、无网络）。
func parseDeepSeekBalance(body []byte) (BalanceResult, error) {
	var parsed deepSeekBalanceResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return BalanceResult{}, fmt.Errorf("deepseek balance: decode: %w", err)
	}
	for _, info := range parsed.BalanceInfos {
		if !strings.EqualFold(strings.TrimSpace(info.Currency), "CNY") {
			continue
		}
		val, err := strconv.ParseFloat(strings.TrimSpace(info.TotalBalance), 64)
		if err != nil {
			return BalanceResult{}, fmt.Errorf("deepseek balance: parse total_balance %q: %w", info.TotalBalance, err)
		}
		return BalanceResult{AvailableCNY: val, IsAvailable: parsed.IsAvailable}, nil
	}
	// 没有 CNY 条目：DeepSeek 正常恒有 CNY，缺失视为异常响应，不当作 0 余额误报。
	return BalanceResult{}, fmt.Errorf("deepseek balance: no CNY balance_info in response")
}

func truncateForErr(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

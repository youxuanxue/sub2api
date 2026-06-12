package service

// TK: 上游账号「低余额」主动预警飞书卡片。
//
// 与本文件的兄弟告警（永久失效红卡 / 临时冷却橙摘要 / 池级 P0）不同，这是一条**预防性**
// 预警——账号尚能服务，只是余额逼近阈值，提前提醒运营充值，避免归零后触发全量 402 断供
// （2026-06-11 DeepSeek 余额归零事故）。故用橙头（警告，非红头 P0）。
//
// 触发判定 + re-arm 去重在 upstream_balance_sentinel_tk.go 的哨兵里（它握有余额数值）；
// 本方法是无状态的「立即渲染并发一条卡」，与 buildAccountIncident*Text 同性质。方法挂在
// 具体类型 *TKAccountIncidentNotifier 上、不进 AccountIncidentNotifier 接口，故不波及接口
// 的若干测试 stub。

import (
	"fmt"
	"strings"
	"time"
)

// NotifyUpstreamBalanceLow 发一条上游账号低余额橙头预警卡。哨兵已完成「首次跨入低于阈值」
// 的去重，这里每调用一次即发一条。
func (n *TKAccountIncidentNotifier) NotifyUpstreamBalanceLow(account *Account, balanceCNY float64, isAvailable bool, threshold float64, rechargeURL string) {
	if n == nil || account == nil {
		return
	}
	title := fmt.Sprintf("TokenKey 上游账号低余额预警 [%s]", n.siteID)
	body := buildUpstreamBalanceLowText(n.siteID, account, balanceCNY, isAvailable, threshold, rechargeURL, n.currentTime())
	n.send(title, "orange", body, "upstream_balance_low")
}

func buildUpstreamBalanceLowText(site string, account *Account, balanceCNY float64, isAvailable bool, threshold float64, rechargeURL string, now time.Time) string {
	rechargeLine := ""
	if r := strings.TrimSpace(rechargeURL); r != "" {
		rechargeLine = "\n**充值**：" + escapeFeishuText(r)
	}
	// 上游自报不可用（DeepSeek is_available=false）= 余额可能已归零，措辞更紧急。
	availabilityNote := ""
	if !isAvailable {
		availabilityNote = "\n**上游状态**：已报账号不可用（余额可能已归零，正在或即将全量 402 断供）"
	}
	return fmt.Sprintf("**节点**：%s\n**账号**：%s\n**平台**：%s\n**组**：%s\n**当前余额**：%.2f 元\n**告警阈值**：%.2f 元%s%s\n**时间**：%s\n\n**建议**：尽快为该上游渠道账号充值；余额归零会把整条线并发压到下限并最终触发全量 402 断供。",
		escapeFeishuText(site),
		escapeFeishuText(accountIncidentLabel(account)),
		escapeFeishuText(strings.TrimSpace(account.Platform)),
		escapeFeishuText(accountGroupNames(account)),
		balanceCNY,
		threshold,
		availabilityNote,
		rechargeLine,
		escapeFeishuText(formatAlertTime(now)),
	)
}

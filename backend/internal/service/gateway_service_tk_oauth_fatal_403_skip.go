package service

// TK：account-fatal 403 跳过同账号原地重试。
//
// 背景：shouldRetryUpstreamError 对 OAuth 账号专门重试 403（gateway_service.go），原地最多
// 打 5 次 / 10s 才换号。对一个**确定性 account-fatal** 的 403（org 封禁 / 空 body 持续被拒）,
// 这 5 次必然全部 403、纯属加重上游封禁——而账号马上就会被熔断器禁用。这里识别这类 403,
// 让调用方跳过原地重试、直奔 failover + 禁用副作用。
//
// 与 #810 / Gap-A 共用判定真值：org-ban 短语经 tkMatchAnthropicOrgBan403Body、空 body 经
// tkIsUnstructuredAnthropicErrorBody——两者都是该 403 会被永久禁用(或落入终局升级)的信号,
// 原地重试它们没有意义。仅限 Anthropic OAuth：其它平台 OAuth 403 的重试语义不变。
func (s *GatewayService) tkIsAccountFatal403(account *Account, body []byte) bool {
	if account == nil || !account.IsOAuth() || account.Platform != PlatformAnthropic {
		return false
	}
	// org 封禁短语(#810 会永久禁用)——原地重试必然继续 403。
	if tkMatchAnthropicOrgBan403Body("", body) != "" {
		return true
	}
	// 空 body / 非结构化 403(Gap-A 持续累计即终局禁用;且本就是逃过短语匹配的 org-ban 形态)。
	if tkIsUnstructuredAnthropicErrorBody(body) {
		return true
	}
	return false
}

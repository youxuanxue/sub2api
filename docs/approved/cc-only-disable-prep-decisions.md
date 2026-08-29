---
title: 关闭 cc-only 时的 canonical Anthropic OAuth 身份闸
status: approved
approved_by: jobs (architect decision doc)
approved_at: 2026-06-10
revised_at: 2026-08-29
authors: [agent]
created: 2026-06-10
related_prs: []
related_commits: []
---

# 关闭 cc-only 时的 canonical Anthropic OAuth 身份闸

关某组 `cc_only` 之后，canonical OAuth 入口不再靠组闸挡住非 CC。身份与 cohort 由下面两个
**正交** setting 承担，均默认 `false`，只作用于 `isCanonicalAnthropicOAuth` 账号。

| 开关 | key | 策略 | 关 cc_only 并放行非 CC 时 |
| --- | --- | --- | --- |
| 入口 UA strict | `anthropic_canonical_ingress_strict_enabled` | reject-at-the-door：allow-list 只放行 `claude-cli/` / `claude-code/`，含空 UA | **OFF** |
| haiku mimicry 补全 | `anthropic_canonical_haiku_mimicry_enabled` | admit-and-launder：非 CC haiku 也做 system/billing 出口补全 | **ON** |

二者不假设一起开关。目标配置只开 haiku 补全：非 CC 可以进来，出口仍洗成 CC cohort。
入口 strict 与「放行非 CC」方向相反，不能绑在同一个开关上。

## 事实

- prod anthropic 账号是 apikey 镜像；真实 OAuth 在 edge。
- prod→edge 透传原样转发客户端 UA，没有内部中继标记。空 UA 是真实客户端信号。
- 默认入口门禁是 deny-list：空/未知 UA 放行。
- 出口 UA 钉死发生在 edge→Anthropic，与入口门禁无关。
- 默认 haiku 跳过 system 重写；非 CC haiku 会变成「有 CC 头、无 CC system/billing」。
- `cc_only` / fallback 仍由 `resolveGatewayGroup` 在组层决定。非 CC→另一池用
  `cc_only=true` + `fallback_group_id`，不在网关重造分流。

## 入口 strict（默认关）

开启时：messages 与 count_tokens 都走 allow-list；空 UA / 未知 UA / 第三方 UA 拒绝。
不发明 `x-tk-relay` 之类可信中继放宽。是否容忍匿名交给这个开关，不写死。

`UpdateSettingsRequest` 用 `*bool` preserve-on-absent，避免旧 admin 整单保存把开关静默打回
false。读取走共享 `gatewayForwardingCache`，不在热路径每请求点查。

strict 403 是客户端身份问题：edge 标 `MarkOpsClientBusinessLimited(LocalPolicyDenied)`；
prod 跨跳用 `canonicalIngressRejectNeedle` + `tkSkipRelayedCanonicalIngressRejectPenalty`
豁免，不把 canary edge 打进 cooldown。先发 prod 豁免，再开 edge strict。

## haiku mimicry（默认关）

开启时，非 CC haiku 也执行 system 重写和 billing block。这是放行非 CC 时的出口必需补丁，
不是入口拒绝。原 haiku 跳过分支保留，新行为只追加。

## 不做

- 不发明 prod→edge 可信中继 header。
- 不重造非 CC→apikey 替代路由。
- 不对非 canonical anthropic OAuth 加入口拦截；最多打漂移告警。
- 不删 deny-list、原 `checkCanonicalIngressUA`、haiku 跳过分支。
- 改线上 `cc_only` 是运营动作，不是本契约的代码路径。

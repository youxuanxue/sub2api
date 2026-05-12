# TokenKey 运营手册：prod cc-uk1-oauth → edge-uk1 配置

> 适用：在 prod admin UI 上配置 `cc-uk1-oauth` 分组、对接 edge-uk1 节点。
>
> 想了解链路代码细节 → 见 [`cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md`](./cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md)。

## 这条链路是什么

```
claude code CLI
   ↓
prod (api.tokenkey.dev) · 命中 cc-uk1-oauth 分组
   ↓
edge-uk1 (api-uk1.tokenkey.dev) · 命中真 Anthropic OAuth 账号
   ↓
api.anthropic.com
```

关键：**prod 这一跳是「TokenKey 网关转发到另一个 TokenKey 网关」**，不是直接对接 Anthropic。所以 prod 上挂的账号必须按下面的"API Key 模式"配。

## ① prod 上的 cc-uk1-oauth 分组

admin UI → 分组 → 编辑 `cc-uk1-oauth`：

| 字段 | 值 |
|---|---|
| Platform | `anthropic` |
| Sticky Routing Mode | `auto`（默认） |
| Claude Code Only | ✅ 勾上 |
| Fallback Group On Invalid Request | 指向一个兜底分组 |
| Model Routing Enabled | ❌ 不勾 |
| Require OAuth Only | ❌ **不勾** |
| RPM Limit | 上游能扛的峰值 × 0.8 |

⚠️ **Require OAuth Only 要关闭**。下面要挂的账号是 API Key 类型，开启它会把账号从调度池里踢出去。

## ② prod 上指向 edge-uk1 的账号

新增账号 → 选择 **"Anthropic Claude Console - API Key"** 模式：

| 字段 | 值 |
|---|---|
| 账号类型 | **Anthropic Claude Console - API Key** |
| Base URL | `https://api-uk1.tokenkey.dev` |
| API Key | 在 edge-uk1 admin UI 上签发的 `tk_xxx` |
| Concurrency | 1～4（跟 edge 能消化的 QPS 匹配） |
| Priority | 多账号建议同优先级，让 LRU 自然均衡 |

⚠️ **不要用 "Anthropic OAuth + Custom Base URL" 模式**。两种模式都能跑通，但 API Key 模式：

- 客户端 header 直接透传，sticky 命中率更高
- 不会做 `metadata.user_id` 改写、不会触发 15 min 会话伪装
- 转发更短更快

⚠️ 如果账号字段里有 **Session ID Masking**，**不要勾**——API Key 模式下不生效，但勾上等于给未来误改回 OAuth 模式埋雷。

## ③ prod 服务器配置

prod 的 `config.yaml`（或 SSM 注入的 config）：

```yaml
security:
  url_allowlist:
    enabled: true
    upstream_hosts:
      - api.anthropic.com
      - api-uk1.tokenkey.dev    # ← 必须有
```

漏掉 `api-uk1.tokenkey.dev` 的症状：**cc-uk1-oauth 所有请求 502**。

## ④ edge-uk1 上的真 Anthropic OAuth 账号

edge 上挂的账号（如 `cc-en-ld-ec2-16-1-a`）是真要把流量送到 Anthropic 的，规则跟 prod 相反：

| 字段 | 值 |
|---|---|
| 账号类型 | **Anthropic OAuth**（真 OAuth，不是 API Key） |
| Session ID Masking | ✅ 勾上 |
| Fingerprint | ✅ 勾上 |

edge 上的分组配置对称：`platform=anthropic`、`sticky_routing_mode=auto`、`claude_code_only=true`、`model_routing_enabled=false`。

## ⑤ 验证

用一台装了 claude code CLI 的机器，配置 base URL 指向 `https://api.tokenkey.dev` + 你的 `tk_xxx` key，跑一个简单对话：

1. 返回 200，输出正常 ✅
2. prod admin UI 的"调度详情"里能看到流量打到你配置的 cc-uk1-oauth 账号 ✅
3. edge-uk1 admin UI 上能看到对应账号有调度记录 ✅
4. 连续跑几条同一对话 → prod 和 edge 两侧**都**应该一直命中同一账号（sticky 生效）✅

第 4 条不成立 → 检查 [§常见坑](#常见坑) 第 2 行。

## 常见坑

| 现象 | 一般原因 |
|---|---|
| cc-uk1-oauth 全部 502 | prod `url_allowlist.upstream_hosts` 漏了 `api-uk1.tokenkey.dev` |
| 同一对话 edge 端被频繁切换账号 | prod 账号被切回 OAuth 模式 + 勾了 Session ID Masking |
| 客户端 401/403 | prod 账号填的 API Key 跟 edge 上签发的 `tk_xxx` 对不上，或 edge 上的 key 被删/禁用 |
| 单对话空闲 > 1 小时后切到新账号 | sticky TTL 默认 1 小时，中间没请求就会过期；**持续在聊不会过期** |
| edge 看到的客户端 IP 全是 prod 的 EIP | 设计如此（edge Caddy 用 prod 远端 IP 改写 XFF），不影响调度 |

## 不在本手册范围

- 代码层调用链 / 函数 / sentinel 细节 → [审计文档](./cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md)
- new-api / OpenAI / Gemini 等其他平台的分组配置
- edge-us1 / sg1（当前未部署）；fra1（法国巴黎，`api-fra1.tokenkey.dev`）见 `deploy/aws/README.md` Edge 小节

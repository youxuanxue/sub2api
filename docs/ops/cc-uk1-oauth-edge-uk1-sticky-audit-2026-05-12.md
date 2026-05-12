# Sticky audit: claude code CLI → prod cc-uk1-oauth → edge-uk1

> 用途：让后续 code agent 在改 sticky / 多跳路由 / outbound builder 相关代码前，快速掌握这条链路的现状、关键代码点、已知约束与雷区。
>
> 基线：base commit `4be7dd9c` + PR #190。
>
> 运营手册（admin UI / 字段配置）：[`cc-uk1-oauth-edge-uk1-config-2026-05-12.md`](./cc-uk1-oauth-edge-uk1-config-2026-05-12.md)

## 1. 链路

```
claude code CLI ─→ prod (api.tokenkey.dev)
                     │  cc-uk1-oauth · platform=anthropic
                     │  prod 账号 = APIKey + base_url=https://api-uk1.tokenkey.dev + tk_xxx
                     │  buildUpstreamRequest line 6027-6035 APIKey 分支
                     │  → x-api-key: edge_tk_xxx
                     │  → body 原样转发，header 白名单透传
                     ▼
                 edge-uk1 (api-uk1.tokenkey.dev)
                     │  Caddyfile.edge 仅放行 prod EIP
                     │  edge 账号 = 真 Anthropic OAuth（cc-en-ld-ec2-16-1-a 等）
                     │  buildUpstreamRequest line 6093+ OAuth 分支
                     │  → mimicry / fingerprint / RewriteUserID / masking
                     ▼
                 api.anthropic.com
```

两端各自的本机 Redis 维护独立 sticky 表 `sticky_session:{groupID}:{sessionHash}`；用 body 中 `metadata.user_id.session_id` 在两跳间对齐。

## 2. 关键代码点

| 主题 | 文件:行 / 符号 |
|---|---|
| sessionHash 派生（priority 1 = metadata.user_id 内 session_id） | `backend/internal/service/gateway_service.go:660 GenerateSessionHash` |
| sticky 派生顺序枚举 | `backend/internal/service/sticky_session_injector.go StickyKeyFromClientHeaders` + `StickyKeySource*` |
| 多跳 sticky preservation | `gateway_service_tk_sticky.go tkEnsureClaudeCodeSessionHeader`，4 个出站 callsite：5333 / 6193 / 6264 / 9392 |
| 出站 builder 分流 | `gateway_service.go:4462 shouldMimicClaudeCode := account.IsOAuth() && !isClaudeCode`；`6033 buildUpstreamRequest` |
| OAuth 专用步骤 | `gateway_service.go:6072 if account.IsOAuth() && s.identityService != nil` → fingerprint / RewriteUserID / masking / CCH |
| masking 实现 | `backend/internal/service/identity_service.go:269 RewriteUserIDWithMasking` |
| sticky-hit TTL 刷新（PR #190 之后） | `gateway_service.go RefreshSessionTTL` 6 callsite：1711 / 1910 / 3074 / 3192 / 3337 / 3457 |
| 出口 sentinel 注册 | `scripts/gateway-tk-sentinels.json`（保护 tkEnsure 4 callsite 字符串） |
| edge Caddy 放行规则 | `deploy/aws/stage0/Caddyfile.edge @allowed_relay remote_ip ${MAIN_GATEWAY_ALLOWED_CIDR}` |
| 部署拓扑 | `deploy/aws/stage0/edge-targets.json` |

## 3. 配置面硬约束

| 约束 | 失败症状 |
|---|---|
| prod cc-uk1-oauth 账号 type = APIKey | OAuth + custom_base_url 模式会进入 OAuth 专用步骤（mimicry / RewriteUserID / masking），行为差异显著（见 §4 C/D） |
| prod `security.url_allowlist.upstream_hosts` 含 `api-uk1.tokenkey.dev` | `validateUpstreamBaseURL`（`gateway_service.go:9419` 一带）拒绝 → 502 |
| 分组 `sticky_routing_mode = auto` | `off` 关沉 sticky；`passthrough` 不派生 |
| 分组 `model_routing_enabled = false` | 开启会走 routing+sticky 路径——PR #190 已修但代码更复杂 |
| edge 账号 type = 真 Anthropic OAuth | edge 是最后一跳，需要 mimicry+fingerprint+masking |

## 4. 已知隐患清单

| ID | 状态 | 描述 | 触发条件 |
|---|---|---|---|
| A | ✅ PR #190 修复 | sticky-hit TTL 不刷新（routing 路径 + 2 个 legacy path） | 任何 sticky hit return |
| B | ✅ PR #190 修复 | Vertex 出口漏 `tkEnsureClaudeCodeSessionHeader` | `account.Type=ServiceAccount` |
| C | ⚠️ 配置约束 | OAuth 路径上 masking 每 15min 轮换 session_id，击穿 edge sticky | prod 账号 type=OAuth + masking=true |
| D | 📘 设计行为 | OAuth 路径 `RewriteUserID` 让两端 sessionHash 不同 | prod 账号 type=OAuth + custom_base_url |
| E | 📘 已知非阻塞 | edge Caddy XFF 覆盖，看不到真实客户端 IP | 永久（设计如此） |
| F | ⚠️ 配置核对 | `url_allowlist` 漏 edge 域名 → 502 | `enabled=true` 且 hosts 缺漏 |

## 5. 改代码时的常见雷区

- **改 sticky 派生顺序** — 动 `GenerateSessionHash` 的优先级会同时影响 prod 和 edge 两侧，验证要两端都看；改完同步检查 `StickyKeyFromClientHeaders` 的 header walk 顺序是否一致。
- **新增 `buildUpstreamRequest*` 变种** — 必须调 `tkEnsureClaudeCodeSessionHeader`，并在 `scripts/gateway-tk-sentinels.json` 加 sentinel，否则下次 upstream merge 会静默删掉。
- **改 TTL refresh 逻辑** — 6 个 callsite 都得动，不要只动一个；现有模式是「sticky hit return 之前刷」。
- **碰 RewriteUserID / masking / fingerprint** — 这些只在 OAuth 路径生效（`if account.IsOAuth()`）；APIKey 账号根本不进，改的时候要确认你在动哪条路径。
- **加新的 sticky 派生源**（比如自定义 header）— 同时改 `StickyKeyFromClientHeaders` 顺序和 `GenerateSessionHash` 顺序，并在 sentinel 注册。
- **改 `validateUpstreamBaseURL` 行为** — 注意 prod→edge 这条链路依赖 `security.url_allowlist.upstream_hosts` 通过校验，不要让默认值变严。

## 6. 调用链分支速查

`shouldMimicClaudeCode := account.IsOAuth() && !isClaudeCode`（line 4462）决定 prod 这一跳走哪条：

| account.IsOAuth() | client UA | mimic | OAuth 专用步骤 | 客户端 header | metadata.user_id |
|---|---|---|---|---|---|
| false (APIKey) | any | false | 全部跳过 | 白名单全透传 | 原样转发 |
| true | claude-cli | false | 部分跳过（fingerprint 跑、RewriteUserID/mimicry 跳） | 白名单全透传 | 改写（masking 时 15min 轮换） |
| true | 非 claude-cli | true | 全跑 | mimic 时整组丢弃，tkEnsure 兜底 | 改写（masking 时 15min 轮换） |

cc-uk1-oauth 的推荐配置（APIKey + base_url）固定第一行。

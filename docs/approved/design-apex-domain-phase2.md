---
title: Apex 域名阶段二 — tokenkey.dev 人类入口 / api.* 机器入口
status: approved
approved_by: feng (对话审批 2026-07-28)
authors: [agent]
created: 2026-07-27
related_prs: ["1477"]
depends_on:
  - deploy/aws/README.md § Apex 域名阶段一（PR #1476，阶段一已落地）
---

# Apex 域名阶段二

## 决策（一句话）

**人类只记 `tokenkey.dev`；机器只连 `api.tokenkey.dev`。**  
阶段二翻转 Caddy：apex 主 serving；`api.*` 仅 gateway / 健康检查 / 服务端 webhook，其余 301 回 apex。

阶段一已完成：DNS、apex LE 证书、apex → api 301。本文件是阶段二的审批基线与 rollout 契约。

## 域名分工（固定，不再讨论）

| 受众 | Canonical host | 用途 |
| --- | --- | --- |
| 浏览器 / 邮件 / SEO | `https://tokenkey.dev` | SPA、Admin、登录、营销、`frontend_url` |
| API 客户端 / CLI / SDK | `https://api.tokenkey.dev` | `/v1/*`、`/v1beta/*`、Codex `/backend-api/codex/*`、Antigravity `/antigravity/*`、OpenAI 根路径别名（`/responses` 等）、`api_base_url` |
| 第三方服务端回调 | `https://api.tokenkey.dev` | OAuth callback、支付 webhook（**不改 host**） |
| Edge 资源节点 | `https://api-<edge>.tokenkey.dev` | **本阶段不动** |

## Caddy 目标形态

```text
tokenkey.dev {
  # 全量 reverse_proxy → tokenkey-{blue|green}:8080（与现 api.* 块相同）
}

api.tokenkey.dev {
  @machine {
    path /health*
    path /v1/*
    path /v1beta/*
    path /api/v1/auth/oauth/*/callback
    path /api/v1/auth/oauth/*/*/callback
    path /api/v1/payment/webhook/*
    path /api/event_logging/batch
    path /backend-api/codex/*
    path /antigravity/*
    path /responses*
    path /chat/completions*
    path /embeddings*
    path /models
    path /images/*
    path /messages/count_tokens
    path /alpha/search
    path /videos*
    path /video/*
  }
  handle @machine { reverse_proxy ... }
  redir https://tokenkey.dev{uri} permanent
}
```

**原则**：`api.*` 用 **allowlist**（`@machine`），不是「猜哪些是人类路径」。新增 gateway 前缀时扩 allowlist；默认一切其它 path 301 到 apex。

Apex 块：删除阶段一的 `redir`，改为与 `api.*` 共享的同构 `reverse_proxy` 块（提取 snippet 或 render 时复用，避免两份 drift）。

## 应用 Settings（prod DB）

| Key | 阶段二值 | 说明 |
| --- | --- | --- |
| `frontend_url` | `https://tokenkey.dev` | 邮件重置密码、外部人类链接 |
| `api_base_url` | `https://api.tokenkey.dev` | **显式填写**（见下「Settings 拍板」）；Quickstart、Keys、OAuth 回调建议 |
| OAuth `*_redirect_url`（服务端） | 保持 `https://api.tokenkey.dev/api/v1/auth/oauth/.../callback` | 与 Caddy allowlist 一致 |
| 支付 notify / return | 保持 `api.tokenkey.dev` | 第三方已登记 URL 零迁移 |

**刻意不改**：Edge 账号 `base_url`、`cc-*` 镜像 stub、prod→edge relay 拓扑。

### Settings 拍板（已确认，2026-07-27）

**`api_base_url` 在 prod 显式设为 `https://api.tokenkey.dev`，不留空。**

两条线，各干各的：

| 线 | 读什么 | 阶段二行为 |
| --- | --- | --- |
| SPA 发请求 | 构建期 `VITE_API_BASE_URL`，默认相对 `/api/v1` | 用户在 apex 上**同源**调 Admin API，**不读** Settings |
| 复制 / 文档 / 回调建议 | Settings `api_base_url`；空则 `window.location.origin` | 必须显式 `api.*`，否则 Quickstart/Keys 会复制成 `tokenkey.dev` |

`api_base_url` 的职责是「复制给 CLI / CC Switch / 回调登记建议」，不是控制网页怎么连后端。留空在 apex 上会把复制板弄脏。

```
frontend_url = https://tokenkey.dev
api_base_url = https://api.tokenkey.dev   # 显式，不留空
```

## 代码 / 静态面（随实现 PR）

| 面 | 改动 |
| --- | --- |
| `deploy/aws/stage0/Caddyfile` + render / sync / CFN blob | apex serve；api.* path 分流 |
| `deploy/aws/stage0/test_render_prod_caddyfile.py` | 阶段二 Caddy 形状断言 |
| `frontend/index.html` | `canonical`、`og:url`、社交图 → `tokenkey.dev` |
| `deploy/aws/README.md` | 新增「阶段二」runbook（与阶段一对称） |

**本 PR 不批量改**：`docs/public/*`、historical ops 笔记、Edge 文档里的 `api.tokenkey.dev` 示例（那些描述的是 **机器入口**，阶段二仍然正确）。单独 chore 可后续整理人类入口文案。

## Rollout 顺序（严格单线，禁止跳步）

```text
1. merge 实现 PR（Caddy + 测试 + index.html meta）
2. bash ops/stage0/sync_caddyfile_via_ssm.sh prod <instance-id>   # AWS_REGION=us-east-1
3. 验收 Caddy（见下）— 通过后再动 Settings
4. Admin 更新 Settings：`frontend_url=https://tokenkey.dev`，`api_base_url=https://api.tokenkey.dev`（显式，不留空）
5. 冒烟 OAuth 登录 + 支付 webhook（只验证已启用通道）
6. 发版（若 index.html 已随 release 镜像带出则 step 6 可合并到 1）
```

**回滚**：`sync_caddyfile` 恢复阶段一 Caddyfile backup；`frontend_url` 改回；无需动 DNS。

## 验收

```bash
# 人类入口
curl -sSI https://tokenkey.dev/login | grep -E '^(HTTP|location):'
# 期望：200（或 302 到已登录态），绝不是 301 到 api.*

# 机器入口仍服务 gateway
curl -fsS https://api.tokenkey.dev/health

# api.* 人类路径被赶出
curl -sSI https://api.tokenkey.dev/login | grep -i location:
# 期望：location: https://tokenkey.dev/login

curl -sSI https://api.tokenkey.dev/admin | grep -i location:
# 期望：location: https://tokenkey.dev/admin

# Settings 公共面
curl -fsS https://tokenkey.dev/api/v1/settings/public | jq -r '.api_base_url'
# 期望：https://api.tokenkey.dev

# LE（两域各一张）
echo | openssl s_client -connect tokenkey.dev:443 -servername tokenkey.dev 2>/dev/null | openssl x509 -noout -subject
echo | openssl s_client -connect api.tokenkey.dev:443 -servername api.tokenkey.dev 2>/dev/null | openssl x509 -noout -subject
```

CI / 运维：`post_deploy_smoke.sh`、`PROD_BASE_URL=https://api.tokenkey.dev` 的 gateway 探测 **保持默认**；阶段二额外加一条 apex `curl` 进 runbook，**不**新增 `SITE_BASE_URL` 环境变量。

## 风险

- **公共契约**：用户书签、分享链接从 api.* 迁到 apex（靠 301 兼容，不是双入口长期并存）
- **第三方**：OAuth / 支付 callback URL 若误改 host → 登录或充值中断
- **Caddy allowlist 漏项**：新 gateway 路径只在 apex 可达、api.* 被 301 → 客户端回归

缓解：allowlist 变更必须带 `test_render_prod_caddyfile.py` 断言；Settings / IdP 变更在 Caddy 验收通过后再做。

---

## 乔布斯审阅

> 审阅标准：聚焦、一条 canonical path、用户不该感知复杂度、能删则删。

### 通过的设计

1. **两个域名，两个受众** — 不是两个产品。用户记 `tokenkey.dev`；开发者记 `api.tokenkey.dev`。这比「什么都挂在 api 子域上」诚实，也比再加 `www`、`app`、`dashboard` 等第三个入口干净。
2. **阶段一 / 二拆分** — 先占证书和 DNS，再_flip serving。没有 big-bang 赌 LE + Settings + Caddy 同夜上线。对。
3. **`api_base_url` 不动** — 成千上万个已复制的 CLI 配置、文档、`ANTHROPIC_BASE_URL` 不应为品牌美学买单。人类入口和机器入口分离，**客户端零迁移**是正确的产品决定。
4. **api.* 默认 301，allowlist 例外** — 默认行为是「这不是给你用的」，比维护一份「人类路径黑名单」更不易腐化。

### 要删 / 不要做的（砍 scope）

| 提议 | 判决 | 理由 |
| --- | --- | --- |
| 新增 `SITE_BASE_URL` 环境变量 | **否** | 验收多一条 `curl tokenkey.dev` 即可；少一个配置面 |
| 阶段二 PR 批量改 `docs/public/*`、ops 历史文档 | **否** | 那些文件里的 `api.tokenkey.dev` 描述的是 **API 地址**，阶段二仍然对；改文档不是改产品 |
| 把 OAuth **前端** redirect 迁到 apex | **否** | 已用相对路径 `/auth/*/callback`；host 随用户落在哪。动 IdP 控制台只为美观 → 风险 > 收益 |
| apex 与 api.* 长期双 full-serve | **否** | 301 负责旧书签；不要「兼容期」变成永久两套入口 |
| Edge 域名 / prod→edge relay 任何改动 | **否** | 无关；混进 PR 必炸 |
| Settings 先于 Caddy 切换 | **否** | 邮件链接先指 apex 而 apex 仍 301 → api → 荒谬链 |

### 已拍板：Settings `api_base_url`

**结论（2026-07-27 确认）：显式 `https://api.tokenkey.dev`，不留空。**

- SPA 已用相对路径 `/api/v1` 同源调 API，与 Settings 无关
- Settings `api_base_url` 只服务 Quickstart/Keys 复制板与 Admin OAuth 回调建议
- 留空在 apex 上会把复制目标变成 `tokenkey.dev` — 一条错指令比没文档伤害更大

### 乔布斯式完成定义

用户打开浏览器输入 `tokenkey.dev`，登录、充值、看文档，**全程不见 api 子域**。  
开发者复制 Quickstart 里的 base URL，**永远是 api.tokenkey.dev**。  
如果有人仍访问 `api.tokenkey.dev/login`，**一次 301 送回 apex，不解释、不弹窗**。

就这些。不要第三域名，不要「过渡期双主页」，不要为文档洁癖开巨型 PR。

---

## 审批门禁

merge 本设计前请确认：

- [x] Caddy allowlist 覆盖所有现网 gateway + webhook 路径（含 `oauth/*/*/callback`、Claude Code `/api/event_logging/batch`）
- [x] Rollout 顺序接受（Caddy → Settings → 人工 OAuth/支付 spot check）
- [x] 接受「公共文档里 api.* 作机器入口示例」暂不批量修改
- [x] Settings 拍板接受：`api_base_url` 显式 `https://api.tokenkey.dev`（不留空）；SPA 仍走相对 `/api/v1`
- [x] 实现 PR 绑定本文件 anchor（`docs/approved/design-apex-domain-phase2.md`）→ PR #1477

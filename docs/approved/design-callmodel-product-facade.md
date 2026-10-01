---
title: CallModel 产品门面（同壳反代 + 品牌铬）
status: approved
approved_by: feng (对话审批 2026-09-30)
authors: [agent]
created: 2026-09-30
revised_at: 2026-10-01
depends_on:
  - docs/approved/design-dual-market-homepage.md
  - docs/approved/design-apex-domain-phase2.md
supersedes_partial:
  - docs/approved/design-dual-market-homepage.md#hostname-allowlist-and-redirect
  - docs/approved/design-apex-domain-phase2.md#应用-settingsprod-db
---

# CallModel 产品门面（同壳反代 + 品牌铬）

## 0. 一句话目标

**两个门面、一个产品内核**：`callmodel.io` 与 `tokenkey.dev` 共用同一 SPA、同一后端、同一账户/余额/Key；仅由 hostname 切换**品牌铬**与**首页模型叙事**。

角色分工（已拍板）：

- **`callmodel.io`**：对外自主注册与收费的主商业门面（CallModel）
- **`tokenkey.dev`**：国内/既有门面 + **唯一 Admin**（TokenKey）
- **`api.callmodel.io` / `api.tokenkey.dev`**：同构机器入口

本文件审批通过后，局部修改（supersede）`design-dual-market-homepage.md` 中「海外 host 仅首页 + 非首页 30x 到 tokenkey.dev」的条款。

## 1. 不变量（仍不许分叉）

| 能力 | 唯一事实来源 |
| --- | --- |
| 用户身份 | 同一 user ID；登录态 Cookie **按当前门面 host 设置**（不同 eTLD+1，**互不共享**） |
| 资产 / Key / 模型目录 / 计费 | 同一后端与数据；Key 在两门面与两 API host 通用 |
| API | `api.tokenkey.dev` 与 `api.callmodel.io` 同构 `@machine` allowlist |
| 发布物 | 同一前端构建、同一 app upstream；禁止第二套前端工程 |
| Admin | **仅** `tokenkey.dev`（见 §2） |

明确不做：

- 第二套 Vue 工程 / 第二套部署
- 市场专用余额、专用 Key、专用 model ID
- GeoIP / `?market=` / market preference Cookie
- 跨门面 SSO / “Same account as TokenKey” 登录说明文案（已拍板：不做）
- 为两门面各维护一套 `frontend_url`（见 §4：单一商业人类入口）

## 2. Hostname 行为

| 请求 | 行为 |
| --- | --- |
| `tokenkey.dev/*`（含 `/admin*`） | TokenKey 门面；Admin **只在这里** |
| `callmodel.io/`、`/home` | CallModel 门面 + china-export 首页叙事 |
| `callmodel.io/<产品路径>` | 同源反代同一 SPA；**不再** 30x 到 `tokenkey.dev` |
| `callmodel.io/admin*` | **302/301 → `https://tokenkey.dev/admin*`**（或 404；实现选 302 保书签） |
| `api.callmodel.io` machine | 与 `api.tokenkey.dev` 同构 |
| `api.callmodel.io` 非 machine | **301 → `https://callmodel.io{uri}`** |
| `api.tokenkey.dev` 非 machine | 仍 301 → `https://tokenkey.dev{uri}` |

灰度：`GLOBAL_SITE_PHASE=candidate|live` = 启用 CallModel **全门面** vhost。回滚：`disabled` 或恢复旧「仅首页 + 非首页 30x」。

## 3. 门面 SSOT

`frontend/src/features/home/marketProfile.tk.ts`（可扩展为 facade owner，仍单文件）：

```text
resolveFacade(hostname) ->
  tokenkey: brand=TokenKey, apiOrigin=https://api.tokenkey.dev, homeProfile=current
  callmodel: brand=CallModel, apiOrigin=https://api.callmodel.io, homeProfile=china-export
```

消费规则：

1. **首页内容**：仅 `homeProfile`；Home 路径渲染分支消费。
2. **品牌铬**：壳层（侧栏/顶栏/登录页/footer/document title）按 `brand`；**允许**非 Home 页面读取 facade（推翻旧「只有 HomeView 可读」）。
3. **站内链接**：门面内一律相对路径；禁止写死踢到另一门面（Admin 例外由 Caddy 执行）。
4. **删除** Router 的 `resolveGlobalProductRedirect` 跨域踢出。
5. API 示例：门面 `apiOrigin` + 验证模型 `deepseek-flash`。

## 4. Settings / 邮件 / 支付（已拍板方向）

CallModel 是对外自主注册与收费的主产品 → **事务性人类链接以 CallModel 为准**：

| 字段 | 决策 |
| --- | --- |
| `frontend_url` | 迁为 `https://callmodel.io`（邮件、支付成功回跳、账单里的「打开控制台」等） |
| `api_base_url` | 迁为 `https://api.callmodel.io`（Quickstart/Keys 复制给终端用户的默认机器入口） |
| OAuth / 支付 **webhook** 回调 | **仍** `api.tokenkey.dev`（machine allowlist；不随门面裂变） |

门面内 UI 不依赖 Settings 生成跨域跳转；只读 `window.location.origin` + facade SSOT。

国内用户若从 `tokenkey.dev` 进入，产品页仍是 TokenKey 铬；其收到的邮件可能带 CallModel 链接——可接受的主商业门面代价。不做「按注册 host 拆两套邮件域名」的首版复杂度。

## 5. 登录与 Cookie

| 场景 | 行为 |
| --- | --- |
| 在 `callmodel.io` 注册/登录 | Cookie 在 `callmodel.io`；完整使用该门面（除 Admin） |
| 跨门面 | 不共享 Cookie；需再登录；**不加**解释文案 |
| 登出 | 只清当前 host |

## 6. Caddy

`callmodel.io`：对齐 apex SPA 反代；去掉非首页 redir 大网兜；**单独** `@admin` → `tokenkey.dev`。

`api.callmodel.io` 非 machine → `callmodel.io`。

## 7. 验证合同

1. `callmodel.io/models`、`/quickstart`、`/register`、`/dashboard` → 200 SPA，品牌 CallModel  
2. `callmodel.io/admin` → 30x 到 `tokenkey.dev/admin`  
3. 首页 CTA 相对路径，不离域  
4. callmodel 登录后 Cookie host=`callmodel.io`  
5. `api.callmodel.io/login` → 301 `callmodel.io/login`；`/v1/*` 不回跳  
6. `tokenkey.dev` 产品与 Admin 无回归  
7. 自动化分工：Playwright 覆盖双门面壳品牌 + CallModel 同 host 产品旅程；Admin 踢出 / API alias 非 machine 回跳由 Caddy render 单测 + `ops/observability/probe-global-candidate.py`（`--api-alias-url` / `--expect-commercial-urls`）覆盖  
8. 发一封测试邮件 / 支付回跳 URL host = `callmodel.io`（改 Settings 后）

## 8. 发布顺序

```text
审批本文件
  -> 实现 PR（含 deepseek-flash 示例若尚未合入）
  -> 发版 app
  -> sync_caddyfile 全门面
  -> Settings frontend_url / api_base_url 切到 CallModel（可同发版窗口，先 app+caddy 再 Settings）
  -> probe + 手工注册/Models/一封邮件抽检
```

## 9. 已拍板决策

1. Admin **仅** `tokenkey.dev`  
2. 邮件 / 支付回执人类入口 → **`callmodel.io`**（`frontend_url` / 面向用户的 `api_base_url` 同步迁）  
3. **不需要** “Same account as TokenKey” 登录说明  

## 10. Jobs 复审（方案级）

### 聚焦

只做一件事：让海外用户在 **CallModel 门面内**完成发现 → 注册 → 付费 → 调用，不再被抛到另一个品牌域名。Admin 留在 TokenKey = 对「运维入口不进商业门面」说了不。通过。

### 简洁

同壳反代 + hostname 品牌铬，比第二套前端更干净。  
曾考虑的「Settings 仍写 tokenkey、邮件却想指向 callmodel」是**双真相**——已否决，改为单一 `frontend_url=callmodel.io`。  
跨域 SSO / 解释文案首版不做 = 少一套状态机。通过。

### 端到端

修复前：CallModel 首页 → 点 Models → TokenKey 壳（品牌断裂）。  
修复后：CallModel 首页 → Models / 注册 / 控制台 / 邮件回跳 同品牌；机器入口 `api.callmodel.io`；Admin 明确在另一门面。完整商业旅程闭合。通过。

### 设计即工作方式

开发者心智：`callmodel.io` = 产品；`api.callmodel.io` = 调用；`tokenkey.dev/admin` = 运营。  
实现上：一个 facade SSOT、相对路径、Caddy 只多一条 Admin 踢出。命名跟现实一致。通过。

### 精品意识 / 诚实残留

- 跨门面仍要再登录：不美，但比假 SSO 诚实；用户主路径留在 CallModel 后很少撞上。可接受。  
- 国内 TokenKey 用户邮件可能带 CallModel 链：主商业门面的代价；若不可接受，唯一干净升级是「注册 host → 邮件 origin」映射（首版明确不做）。  
- OAuth/webhook 仍钉在 `api.tokenkey.dev`：机器回调与人类门面分离，正确。

### 复审结论

**方案可自豪展示，建议批准后实现。**  
唯一实现期纪律：Settings 切换与 Caddy/App 同窗口验证邮件 host，禁止长期停在「新门面 + 旧 frontend_url」中间态。

# US-046-public-docs-content-ssot

- ID: US-046
- Title: 首页、模型页和登录页可达的公开文档内容 SSOT
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **未登录或刚注册的 TokenKey 用户**，我希望 **从首页、模型/价格页和登录页进入同一套 Quickstart、SDK、客户端、错误码和媒体任务文档**，**以便** 无需猜入口，也不会在公开文档与登录后配置指导之间读到两套答案。
- Trace:
  - 设计锚点：`docs/approved/p0-conversion-trust.md` §10。
  - Goal：`docs/task-breakdown-p0-conversion-trust-goals.md` P0-G6。
  - 现有 authenticated surface：`frontend/src/views/user/QuickstartView.vue`、`frontend/src/components/keys/UseKeyGuide.vue`、`frontend/src/constants/clientIntegrations.tk.ts`。
- Risk Focus:
  - 逻辑错误：新增 public Markdown/页面复制 client/protocol/snippet，之后与 authenticated Quickstart 漂移。
  - 行为回归：`doc_url` 未配置、API 故障或 `home_content` 全页覆盖时 canonical docs 入口消失。
  - 安全问题：authenticated substitution 把真实 API key 写入 URL、analytics、local docs cache、源码或静态 artifact。
  - 运行时：文档依赖后端 API 才能首屏渲染，服务故障时用户连排障文档也打不开。

## Acceptance Criteria

1. **AC-001（canonical routes）**：Given未登录用户，When访问 `/docs/quickstart`、`/docs/sdk`、`/docs/clients`、`/docs/errors`、`/docs/media`、`/docs/compatibility`，Then所有路由无需鉴权且刷新直达可用。
2. **AC-002（强制入口）**：Given默认或 custom home、`/models`、`/pricing`、`/login`，When首屏/全局 trust nav 渲染，Then均有 first-party Quickstart/docs 入口，不依赖 `doc_url`。
3. **AC-003（一个 registry）**：Given public docs 与 authenticated `/quickstart`，When渲染 client/protocol/snippet/limitation/error 内容，Then都引用同一 typed versioned content registry 与 client integration catalog，无复制列表。
4. **AC-004（安全 substitution）**：Given public context，When渲染代码片段，Then只出现 `YOUR_TOKENKEY_API_KEY`；Given authenticated context，When插入所选 key，Then仅存在组件内存和可见 code block，不进入 URL、analytics、持久 docs state 或 artifact。
5. **AC-005（覆盖内容族）**：Given docs manifest，When进行 route completeness 检查，Then至少覆盖 Quickstart、OpenAI/Anthropic SDK、主流 clients、稳定 public error codes、异步 media submit/status/result 与 compatibility limitation 解释。
6. **AC-006（离线可读）**：Given public settings/catalog/compatibility API 故障，When打开静态文档，Then内容 shell 和基础指南仍渲染；动态证据只显示 unavailable，不阻塞排障文档。
7. **AC-007（external docs 边界）**：Given合法 `doc_url`，When渲染，Then它只作为 sanitized secondary extended-docs link；缺失/非法时 canonical local docs 完整保留。
8. **AC-008（共享版本 gate）**：Given任何内容或 client catalog 修改，When运行 contract tests，Then public/authenticated renderer 的 content version、route manifest、内部链接和 snippet fixtures 一致，否则失败。

## Assertions

- 文档内容 registry 是前端构建资产，不从任意 admin HTML 或远程 Markdown 执行脚本。
- 登录后 Quickstart 的个性化仅是安全 placeholder substitution，不形成第二份 prose/配置真相。
- custom `home_content` 外仍保留 first-party trust nav。
- 文档发布与 compatibility API 发布可独立回滚；静态 docs shell 始终保留。

## Linked Tests

- `frontend/src/content/__tests__/tokenkeyDocsRegistry.spec.ts`::`manifest owns every canonical route and content family` *(planned)*
- `frontend/src/content/__tests__/tokenkeyDocsRegistry.spec.ts`::`public and authenticated renderers share one content version` *(planned)*
- `frontend/src/content/__tests__/tokenkeyDocsRegistry.spec.ts`::`real keys never enter persisted documentation surfaces` *(planned)*
- `frontend/e2e/public-docs.spec.ts`::`home models pricing and login reach canonical docs while logged out` *(planned)*
- `frontend/e2e/public-docs.spec.ts`::`custom home and API failure keep static docs reachable` *(planned)*

运行命令：

```bash
cd frontend && npm run test:unit -- tokenkeyDocsRegistry.spec.ts
cd frontend && npm run test:e2e -- public-docs.spec.ts
```

## Evidence

- 实现 PR 附 route/link contract、public/authenticated visual evidence、offline API-failure journey 和 key-pattern scan。

## Status

- Ready — 等待设计批准；公开 route shell 不依赖 registration 或 compatibility 发布。

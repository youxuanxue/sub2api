---
title: Supplier Source - discover, validate, save, and project
status: approved
approved_by: "xuejiao (operator directives 2026-09-01; revised 2026-09-02)"
approved_at: 2026-09-01
updated: 2026-09-02
created: 2026-09-01
owners: [tk-platform]
scope: "amend US-048 / model-supplier-source-management: four explicit actions; project owns its immediate write-gate probe; legacy probe routes remain aliases"
related_stories: ["US-048"]
amends: ["docs/approved/model-supplier-source-management.md"]
related_design: ["docs/approved/model-supplier-source-fmgo-seedance-account-rewrite.md"]
operator_locks: "2026-09-02: discover -> validate -> save -> project; validate is read-only preview, never write authorization; project re-probes immediately before structural writes; no validation cache/token; POST .../probe and probe job remain discover aliases; Anthropic channel_type=14 messages-only probe/project"
---

# 供应源：发现 / 校验 / 保存 / 投影

## 一件事

把供应源页面的四个运营意图拆清楚，同时让账号写入门禁留在写路径自身：

```text
发现模型 -> 校验模型 -> 保存 -> 投影账号
```

四个按钮是职责顺序，不是可复用的授权状态机。`Validate` 提供只读预览；`Project` 对结构变化在写入前
重新探测本次将投影的已保存模型。进程重启、多实例切换或时间经过都不能绕过这个门禁。

FMGo Seedance 账号侧动态改写不在本文件范围，见
`docs/approved/model-supplier-source-fmgo-seedance-account-rewrite.md`。

## 已拍板

| 项 | 决定 |
|---|---|
| 按钮顺序 | **发现模型 -> 校验模型 -> 保存 -> 投影账号** |
| Validate | 只读探测已保存配置；结果只用于当下判断，不授权未来写入 |
| Project | 结构变化 / 非空 mapping 投影前即时重探；失败不写账号 |
| 元数据快路 | 名称、priority、concurrency 等不改变结构时可不探测 |
| 缓存 / token | 不存在 validation cache、TTL、probe token 或证据表 |
| 新 API | `discover`、`discover job`、`validate`、`sync` |
| 兼容 API | 旧 `probe`、`probe job` 路由继续注册，复用 Discover handler |
| `models-discover` | 管理路由保持注销；不留 410 占位 |
| 脏表单 | 发现、校验、投影禁用，先保存 |
| 写边界 | 发现/校验不写库和账号；保存只写供应源；投影只窄写受管账号 |

## 行为

### 发现模型

- 基于已保存供应源拉取上游 models、规整已配置 ID，并异步探测未配置候选。
- 不探测已配置行，不写供应源，不写账号。
- 规整回填或运营加入建议后表单变脏，只能先保存。
- `POST .../probe` 与 `GET .../probe/jobs/:job_id` 是兼容别名，行为与 Discover 相同。

### 校验模型

- 基于已保存供应源逐行真实探测已配置模型。
- 返回完整 `probe_results` 和 `failed_step`，不写任何状态。
- 成功结果不缓存、不生成授权，不影响 Project 是否可点。

### 保存

- 只把当前采购事实写入供应源表。
- 表单与库一致时禁用保存；保存不会探测或投影账号。

### 投影账号

- 只读取库内已保存事实，不消费 Discover/Validate 的浏览器或进程状态。
- 结构变化时，在第一个结构写入之前用当次目标账号逐模型重探。
- 任一探测失败，返回完整当次 `probe_results`、`failed_step=probe`、空 `changes`，不创建或更新账号。
- 全部成功后，才按先增后减规则执行窄写；非空 mapping 携带本次探测证据。
- 失败后重试会重新探测，不能复用上次成功或失败结果。

## API

```text
POST /admin/supplier-sources/:id/discover
GET  /admin/supplier-sources/:id/discover/jobs/:job_id
POST /admin/supplier-sources/:id/validate
POST /admin/supplier-sources/:id/sync

# compatibility aliases
POST /admin/supplier-sources/:id/probe
GET  /admin/supplier-sources/:id/probe/jobs/:job_id
```

`discover` 和兼容 `probe` 共享一个 service owner；job 查询同样共享。禁止复制第二套发现状态机。

## 探测协议

- 默认：Chat Completions 正向证据。
- 视频通道（DoubaoVideo / 54）：走该通道真实视频方言，不用 `hi` Chat 冒充成功。
- Anthropic 通道（14）：走 `/v1/messages` 正向证据（CloudWise Claude opus/sonnet 上游仅 messages）；
  不用 Chat Completions 冒充成功；投影账号仅声明 Messages。
- 每一行用该行 `upstream_model_id` 探测；失败封闭。
- 不按 client 名字猜 transport，不建设通用协议配置器。

## 状态矩阵

| 表单状态 | 发现 | 校验 | 保存 | 投影 |
|---|---|---|---|---|
| 干净（与库一致） | 可 | 可 | 禁 | 可 |
| 手改未保存 | 禁 | 禁 | 可 | 禁 |
| 发现回填草稿 | 禁 | 禁 | 可 | 禁 |

Validate 成功或失败都不改变矩阵。Project 未先 Validate 仍可执行，因为它拥有自己的即时探测门禁。

## 验收场景

1. 干净表单直接 Project：结构变化路径先探测；成功才写账号。
2. 干净表单直接 Project：探测失败返回完整结果且零账号写；上游恢复后重试重新探测并成功。
3. 先 Validate 成功、进程重启或请求落到另一实例、再 Project：Project 仍重新探测。
4. 仅名称、priority 或 concurrency 漂移：Project 走元数据快路，不做模型探测。
5. 旧客户端调用 `/probe` 与 probe job：仍得到 Discover 结果，不出现 404。
6. 表单脏：发现、校验、投影都禁用，只能先保存。

## 验证

```bash
go test -tags=unit ./internal/service -run 'US048|SupplierSource|Probe|Discover|Validate'
go test -tags=unit ./internal/handler/admin ./internal/server/routes -run 'US048|SupplierSource'
pnpm --dir frontend exec vitest run src/views/admin/__tests__/SupplierSourcesView.spec.ts
pnpm --dir frontend exec playwright test e2e/us048-supplier-source-management.e2e.ts
```

Playwright 必须覆盖未先 Validate 的 Project 失败、零写入和重试恢复；API-only 测试不能代替该 UI 流程。

## 非目标

- 不新增 validation cache、授权 TTL、probe token、探测历史表或分布式锁。
- 不把 `supplier_source_id` 引入 scheduler / gateway。
- 不做通用全协议配置器。
- 不重新注册 `models-discover`。

## 审批检查清单

- [x] 四按钮顺序固定
- [x] Validate 只读，不是写授权
- [x] Project 在结构写入前即时重探
- [x] 旧 `/probe` 路由保留为兼容别名
- [x] 无 validation cache / token
- [x] Playwright 覆盖 Project-before-Validate 失败与恢复
- [x] 2026-09-02 运营修订已批准

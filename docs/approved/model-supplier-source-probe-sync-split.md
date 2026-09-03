---
title: Supplier Source - discover, validate, save, and project
status: approved
approved_by: "xuejiao (operator directives 2026-09-01; revised 2026-09-02; orthogonal project 2026-09-03)"
approved_at: 2026-09-01
updated: 2026-09-03
created: 2026-09-01
owners: [tk-platform]
scope: "amend US-048 / model-supplier-source-management: four orthogonal actions; validate owns model probe; project writes accounts only; legacy probe routes remain aliases"
related_stories: ["US-048"]
amends: ["docs/approved/model-supplier-source-management.md"]
related_design: ["docs/approved/model-supplier-source-fmgo-seedance-account-rewrite.md"]
operator_locks: "2026-09-03: discover / validate / save / project are orthogonal; validate owns model probe; project never probes; no validation cache/token; POST .../probe and probe job remain discover aliases; Anthropic channel_type=14 messages-only validate/project"
---

# 供应源：发现 / 校验 / 保存 / 投影

## 一件事

把供应源页面的四个运营意图拆成独立、正交的动作：

```text
发现模型 | 保存 | 校验模型 | 投影账号
```

四个按钮各做一件事，互不授权、互不复用结果。`Validate` 是唯一的已配置模型探测；`Project`
只把库内已保存事实写入受管账号，不探测。进程重启、多实例切换或时间经过都不能让投影去探测。

FMGo Seedance 账号侧动态改写不在本文件范围，见
`docs/approved/model-supplier-source-fmgo-seedance-account-rewrite.md`。

## 已拍板

| 项 | 决定 |
|---|---|
| 按钮顺序 | **发现模型 -> 保存 -> 校验模型 -> 投影账号** |
| Validate | 只读探测已保存配置；结果只用于当下判断，不授权未来写入 |
| Project | 只窄写受管账号；不探测模型，也不消费 Validate 结果 |
| 元数据快路 | 名称、priority、concurrency 等不改变结构时只改元数据 |
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
- 不探测已配置模型；`probe_results` 为空。
- 结构变化按先增后减规则窄写受管账号；非空 mapping 按 `channel_type` 声明单一协议能力。
- 写失败返回 `failed_step + changes[]`，重试重新计算差异并继续收敛。

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

本节只约束发现/校验。投影不探测，只按 `channel_type` 声明协议能力。

- 默认：Chat Completions 正向证据。
- 视频通道（DoubaoVideo / 54）：走该通道真实视频方言，不用 `hi` Chat 冒充成功。
- Anthropic 通道（14）：走 `/v1/messages` 正向证据（CloudWise Claude opus/sonnet 上游仅 messages）；
  不用 Chat Completions 冒充成功；投影账号仅声明 Messages。
- 每一行用该行 `upstream_model_id` 探测；失败封闭。
- 不按 client 名字猜 transport，不建设通用协议配置器。

## 状态矩阵

| 表单状态 | 发现 | 保存 | 校验 | 投影 |
|---|---|---|---|---|
| 干净（与库一致） | 可 | 禁 | 可 | 可 |
| 手改未保存 | 禁 | 可 | 禁 | 禁 |
| 发现回填草稿 | 禁 | 可 | 禁 | 禁 |

Validate 成功或失败都不改变矩阵。Project 未先 Validate 仍可执行，因为它不探测、也不依赖校验结果。

## 验收场景

1. 干净表单直接 Project：不探测，结构变化直接写账号。
2. 干净表单直接 Project：即使 Validate 会对某行失败，Project 仍写账号且 `probe_results` 为空。
3. 先 Validate 失败、再 Project：Project 仍写账号，不复用也不重放校验结果。
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

Playwright 必须覆盖未先 Validate 的 Project 直接成功写入；API-only 测试不能代替该 UI 流程。

## 非目标

- 不新增 validation cache、授权 TTL、probe token、探测历史表或分布式锁。
- 不把 `supplier_source_id` 引入 scheduler / gateway。
- 不做通用全协议配置器。
- 不重新注册 `models-discover`。

## 审批检查清单

- [x] 四按钮顺序固定
- [x] Validate 只读，不是写授权
- [x] Project 不探测，只写受管账号
- [x] 旧 `/probe` 路由保留为兼容别名
- [x] 无 validation cache / token
- [x] Playwright 覆盖未先 Validate 的 Project 直接写入
- [x] 2026-09-03 运营修订：四步正交，探测只属于校验模型

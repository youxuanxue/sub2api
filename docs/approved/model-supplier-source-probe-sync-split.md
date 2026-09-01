---
title: Supplier Source — split Probe vs Sync Accounts
status: approved
approved_by: "xuejiao (operator directives 2026-09-01)"
approved_at: 2026-09-01
updated: 2026-09-01
created: 2026-09-01
owners: [tk-platform]
scope: "amend US-048 / model-supplier-source-management: independent Probe and Sync; one public probe path; form draft from probe then save only"
related_stories: ["US-048"]
amends: ["docs/approved/model-supplier-source-management.md"]
related_design: ["docs/approved/model-supplier-source-fmgo-seedance-account-rewrite.md"]
operator_locks: "2026-09-01: probe→save→sync; dirty form disables probe and sync; clean form disables save; sync re-probes; one public POST .../probe; same PR unregisters models-discover; channel capability selects probe protocol"
---

# 供应源：探测校验 / 保存 / 同步账号（审批草案）

## 一件事

把「校验并同步」拆成三个动词，且对外只留一条探测主路径。

FMGo Seedance 账号侧动态改写不在本文件范围，见  
`docs/approved/model-supplier-source-fmgo-seedance-account-rewrite.md`。

本文件已批准，实现按本文执行。

## 已拍板

| 项 | 决定 |
|---|---|
| 按钮顺序 | **探测校验 → 保存 → 同步账号** |
| 同步是否再测 | **要**。结构变化 / 非空 mapping 投影前仍门禁探测；失败不写账号 |
| 探测 API | **P1** `POST .../probe` 为页面唯一探测入口；内部唯一实现 |
| `models-discover` | **同 PR 撤管理路由**；单测打内部函数，不留 410、不靠僵尸 HTTP |
| 探测回填草稿后 | **禁用探测与同步，只留保存** |
| 手改未保存 | 探测、同步都禁用，先保存 |
| 表单与库一致 | **禁用保存**；探测、同步可点 |
| 探测协议 | **通道能力**决定协议（如 `channel_type=54` → 视频门禁）；**行**决定探哪个 upstream |
| 写边界 | 探测：不写库、不写账号，只可回填页面草稿并标变更；保存：只写供应源表；同步：只窄写受管账号 |
| 同步投影 | 投影库里已保存的 mapping 行，不做 inventory 展开 |

## 运营路径（唯一故事）

```text
探测校验 →（若有草稿变更）保存 → 同步账号
```

- **探测校验**：基于已保存供应事实，拉上游、规整建议、测已配置行可服务性；可把规整/建议写入表单草稿并标出变更；账号不变。
- **保存**：草稿落库。表单干净时按钮禁用。
- **同步账号**：只读库内事实 →（必要时）再测 → 投影账号。scheduler / gateway 仍只认账号。

探测始终验库，不验未保存草稿。回填后必须先保存。

结果区两个标题：「上游发现」「已配置可服务性」。仍是一个按钮。

## Delta

### ADDED — 三按钮

| 顺序 | 文案 | 写供应源 | 写账号 | 职责 |
|---|---|---|---|---|
| 1 | 探测校验 | 否（可改页面草稿） | 否 | 发现 + 已配置门禁探测 |
| 2 | 保存 | 是 | 否 | 采购事实落库 |
| 3 | 同步账号 | 否 | 是 | 再测（如需）+ 窄写投影 |

### MODIFIED — 脏表单（无特例）

| 表单状态 | 探测校验 | 保存 | 同步账号 |
|---|---|---|---|
| 干净（与库一致） | 可 | 禁 | 可 |
| 手改未保存 | 禁 | 可 | 禁 |
| 探测回填产生的草稿 | 禁 | 可 | 禁 |

建议追加未点「加入表单」时表单仍干净：探测、同步可点。  
回填后提示「请先保存，再同步」；不暗示草稿已被探测通过。

### MODIFIED — API（一条探测主路径）

```text
POST /admin/supplier-sources/:id/probe
GET  /admin/supplier-sources/:id/probe/jobs/:job_id
POST /admin/supplier-sources/:id/sync
```

- `probe`：只读；= list/规整/候选探测 ∪ 已配置行门禁；返回发现字段 + `probe_results` + `failed_step`；永不写供应源表、永不写账号。
- discover + 候选探测的**唯一**实现挂在 service 内部函数；`probe` 调用它。
- `POST .../models-discover` 与 `.../models-discover/jobs/:id`：**同 PR 从管理路由注销**。禁止 410 占位。测试打内部函数。
- `sync`：不前置 discover/probe 编排；结构变化时自带门禁探测；仅元数据漂移可不探。无 `probe_token` / 证据表。

### MODIFIED — 探测协议

- 默认：Chat Completions 正向证据。
- 通道能力为视频（DoubaoVideo / 54）→ 该通道真实视频方言，不得用「hi」Chat 伪成功。
- FMGo `feimiao-v2` 的真实视频方言就是官方异步 `POST /v1/chat/completions`；Ark / XRToken 仍走各自视频口。
- 每一行用该行 `upstream_model_id` 探测；失败封闭，不写账号。
- 不按 client 名字开协议例外。

### REMOVED

- 「校验并同步」单按钮与同步前自动 discover。
- 管理路由上的 `models-discover`（含 job）。
- 「探测回填后仍可再探测」特例。
- 干净表单仍可点保存。

## Scenarios

### 正向

1. 已保存、表单干净 → 探测 → 见发现与已配置探测结果 → 账号不变；保存禁用。
2. 探测回填草稿 → 仅保存可点 → 保存后表单干净 → 再同步；同步路径再测。
3. 仅改 `base_priority` 并保存 → 同步可跳过模型探测，只改名/priority。

### 负向

1. 手改或探测草稿未保存 → 探测、同步禁用。
2. 已配置任一行门禁失败 → 全量结果展示，账号不写；保存后同步若仍需结构探测，再次失败封闭。
3. 建议追加未加入表单并保存 → 不进库，不同步进账号。

### 回归

- Chat 类供应源：门禁语义与拆分前一致；不再强制先 discover。
- 账号托管徽标：覆盖触发改为「同步账号」。

## Validation

```bash
go test -tags=unit ./internal/service/ -run 'US048|SupplierSource|Probe|Discover'
pnpm --dir frontend exec vitest run src/views/admin/__tests__/SupplierSourcesView.spec.ts
pnpm --dir frontend exec playwright test e2e/us048-supplier-source-management.e2e.ts
```

断言重点：三按钮顺序与禁用矩阵（干净禁保存）；`sync` 不调用 discover 编排；管理面无 `models-discover` 路由；probe 失败不写账号。

## 非目标

- 不实现 Seedance SKU 动态改写（另页）。
- 不新增状态机、探测历史表、分布式锁、`probe_token`。
- 不把 `supplier_source_id` 引入 scheduler / gateway。
- 不做通用全协议配置器。
- 不在本页规定某供应源存 16 行还是 2 行。

## 审批检查清单

- [x] 按钮顺序探测 → 保存 → 同步
- [x] 探测回填后只留保存；干净表单禁用保存
- [x] sync 自带再测
- [x] 对外只留 probe；同 PR 撤 models-discover 路由
- [x] 通道能力选协议，行选 upstream
- [x] Seedance 实现另页
- [x] 已批准（2026-09-01）

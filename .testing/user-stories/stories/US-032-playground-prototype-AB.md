# US-032-playground-prototype-AB

- ID: US-032
- Title: 内置 Playground prototype-first（Vue 组件，4 状态对齐）
- Version: V1.6 (cold-start P1-B prototype)
- Priority: P1
- As a / I want / So that:
  作为 **TokenKey 设计审批人 + 工程实施人**，我希望 **在写一行 Playground 后端 wiring 之前先看到 4 个画面（empty / typing / responded / error）的视觉级 prototype**，**以便** 我能基于看得见的形态做拍板（文字描述与最终 UI 偏差最大），同时让后续 PR 的实装（路由 / 鉴权 / fetch / e2e）有一份「不能偏离的 props/state 基线」可以参照。
  
  按 `docs/approved/user-cold-start.md` §11 的内部审批门禁，本 PR **不**实装 Playground 主功能；prototype 以 Vue 组件 + Vitest 断言承接，后续 PR 才进入路由 + 鉴权 + e2e。

- Trace:
  - 角色 × 能力轴线：审批人 × 「直接看 4 个状态并基于可执行组件定稿」。
  - 实体生命周期轴线：Playground 状态机 `empty → typing → responded` 与 `* → error` 两条边，必须在 prototype 阶段就给出视觉。
  - 系统事件轴线：用户从 `/dashboard` 点击「在 Playground 中尝试」（设计文档中的卡片按钮，PR 1 是灰显占位）→ 进入 `/playground` 路由——本 PR **不**激活该按钮，但 prototype 给出激活后的目标形态。
  - 防御需求轴线：4 个状态的 DOM 契约必须由 Vitest 直接锁定，避免静态 mockup 与实现产生双轨漂移。

- Risk Focus:
  - 逻辑错误：Vue prototype 的状态枚举、文案、按钮、字段顺序必须由测试锁定；任何漂移会让"prototype 决策"失去意义。
  - 行为回归：prototype 不引入路由 / 不引入 sidebar 入口 / 不引入新后端路由 / 不引入新 API 调用；只保留 `frontend/src/components/playground/` 下的组件与测试。
  - 安全问题：prototype 里所有"用户输入 / API 响应"都是 hard-coded 字符串，**禁止**任何 fetch / XHR / iframe；仅纯静态展示。
  - 运行时问题：Vue prototype 作为 Vitest component test 装载，必须在 jsdom 中可独立 mount（不依赖 Pinia store / vue-router 全局），方便后续 e2e 复用同 props 形态。

## Acceptance Criteria

1. **AC-001 (A 件存在 + 4 状态)**：Given 仓库根目录，When 列出 `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`，Then 文件存在且 `describe("PlaygroundPrototype")` 内含 4 个 `it("renders ${state} state")`，state ∈ {`empty`, `typing`, `responded`, `error`}；每个 `it` 都 `mount()` `PlaygroundPrototype.vue` 并断言关键 DOM（如 empty 态有 `[data-testid="placeholder"]`，error 态有 `[data-testid="error-banner"]`）。
2. **AC-002 (A 件可独立 mount)**：Given Vitest 在 jsdom 中执行 AC-001 中的 `it`，When 不挂 Pinia / 不挂 vue-router / 不连后端，Then 所有 4 个 it 全绿；这保证 prototype 是"无依赖的视觉胶片"，后续 e2e 可直接复用 props/state 结构。
3. **AC-003 (状态枚举集中)**：Given `PlaygroundPrototype.vue`，When 读取 `PlaygroundState`，Then 只包含 `empty | typing | responded | error` 四个状态；新增状态必须同步补充组件断言。
4. **AC-004 (关键文案 / DOM 契约锁定)**：Given Vitest 执行，When 4 个状态分别 mount，Then input placeholder、assistant 回答示例、error 文案、用量小条等关键 DOM 由测试断言覆盖。
5. **AC-005 (无外部依赖)**：Given prototype 组件，When 在 jsdom mount，Then 不依赖 Pinia / vue-router / 后端 API，也不发起 fetch / XHR / iframe。
6. **AC-006 (不动主路由 / 主 sidebar)**：Given 本 PR diff，When 检查 `frontend/src/router/index.ts` 与 sidebar 组件，Then **没有**新增 `/playground` 路由、**没有**新增 sidebar 入口；prototype 不掺入主流程；后续 follow-up PR 才上路由。
7. **AC-007 (设计问题显式收敛)**：Given prototype 契约，When 阅读 `docs/approved/user-cold-start.md` §11.2 与 `PlaygroundPrototype.vue`，Then system prompt / reasoning / multi-turn / per-call limits / mobile breakpoint 的首版取舍在 approved 文档中可追踪，组件只承接视觉状态，不维护第二份静态决策副本。
8. **AC-008 (回归 / 现有前端测试)**：Given 本 PR 落地，When 跑 `pnpm test:run`，Then 现有所有测试通过（不动现有断言）。

## Assertions

- `frontend/src/components/playground/PlaygroundPrototype.vue` 存在，导出一个接受 `props: { state: 'empty' | 'typing' | 'responded' | 'error' }` 的组件。
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts` 4 个状态 `it` 全绿。
- `frontend/src/router/index.ts` 与 `frontend/src/views/layout/AppSidebar.vue`（或现有 sidebar 文件）**未被本 PR 修改**（`git diff --name-only origin/main..HEAD` 不含这两个文件）。

## Linked Tests

- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`PlaygroundPrototype`
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`renders empty state`
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`renders typing state`
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`renders responded state`
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`renders error state`

运行命令：

```bash
cd frontend && pnpm vitest run src/components/playground/__tests__/PlaygroundPrototype.spec.ts
```

## Evidence

- 可执行证据：`frontend/src/components/playground/PlaygroundPrototype.vue` + `__tests__/PlaygroundPrototype.spec.ts`
- 视觉证据以 `PlaygroundPrototype.vue` 与 Vitest 状态断言为准；不再保留静态 attachment 副本。

## Status

- [x] InTest — `PlaygroundPrototype.vue` + 4 个 Vitest 状态断言全绿；未引入 `/playground` 路由 / sidebar 入口（AC-006 通过 `git diff --name-only` 核对）。后续以实装页与 e2e 承接真实体验，不再维护静态 HTML attachment 副本。

## 实施差异说明

- **不引入 Storybook**：当前 `frontend/package.json` 没有 Storybook 依赖（实测 `pnpm list @storybook` 无结果）；为单纯展示 prototype 引入完整 Storybook 工具链会让 PR 体积膨胀（额外 ~80 个 dev deps + 一个 build target），违反 PR 单一意图原则。改用 **Vitest component test + 自包含 Vue 组件** 的形态：
  - 工程对接价值（A 件价值核心）保留：组件文件 `PlaygroundPrototype.vue` 暴露真实 `props.state`，后续实装 PR 直接复用同 props 结构 + 拓展为 `PlaygroundView.vue`。
  - 视觉过审价值由 Vue component + Vitest DOM 断言承接，不再维护静态 HTML 副本，避免 prototype 与实现双轨漂移。
  - 这是对设计文档 §11.1 表格中 "Storybook story（必做）" 一行的等价替代——形态变化但价值约束不变。后续若团队引入 Storybook，可零成本将 `PlaygroundPrototype.vue` 包装成 `*.stories.ts`。

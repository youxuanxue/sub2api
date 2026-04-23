# US-032-playground-prototype-AB

- ID: US-032
- Title: 内置 Playground prototype-first（A: Vue 组件 stories + B: 静态 HTML mockup，4 状态对齐）
- Version: V1.6 (cold-start P1-B prototype)
- Priority: P1
- As a / I want / So that:
  作为 **TokenKey 设计审批人 + 工程实施人**，我希望 **在写一行 Playground 后端 wiring 之前先看到 4 个画面（empty / typing / responded / error）的视觉级 prototype**，**以便** 我能基于看得见的形态做拍板（文字描述与最终 UI 偏差最大），同时让后续 PR 的实装（路由 / 鉴权 / fetch / e2e）有一份「不能偏离的 props/state 基线」可以参照。
  
  按 `docs/approved/user-cold-start.md` §11 的内部审批门禁，本 PR **不**实装 Playground 主功能；仅交付 prototype A + B 两件，并在 follow-up PR 才进入路由 + 鉴权 + e2e。这样做的代价是延迟一次 PR；好处是避免「实装好了之后被审批驳回 → 改一遍 → 又驳回」的浪费。

- Trace:
  - 角色 × 能力轴线：审批人 × 「在不启动 dev server 的情况下逐画面定稿」——B 件（静态 HTML）就是为这个能力存在的。
  - 实体生命周期轴线：Playground 状态机 `empty → typing → responded` 与 `* → error` 两条边，必须在 prototype 阶段就给出视觉。
  - 系统事件轴线：用户从 `/dashboard` 点击「在 Playground 中尝试」（设计文档中的卡片按钮，PR 1 是灰显占位）→ 进入 `/playground` 路由——本 PR **不**激活该按钮，但 prototype 给出激活后的目标形态。
  - 防御需求轴线：A/B 必须视觉一致（同 4 状态、同文案、同字段顺序、同色变量），否则违反设计文档 §11.1 的「A 与 B 的内容必须一致」约束。

- Risk Focus:
  - 逻辑错误：A（Vue stories）与 B（HTML mockup）的状态枚举、文案、按钮、字段顺序、色值必须一一对应；任何漂移会让"prototype 决策"失去意义。
  - 行为回归：prototype 不引入路由 / 不引入 sidebar 入口 / 不引入新后端路由 / 不引入新 API 调用；只新建 `frontend/src/components/playground/` 与 `docs/approved/attachments/playground-prototype-2026-04-XX.html`。
  - 安全问题：HTML mockup 里所有"用户输入 / API 响应"都是 hard-coded 字符串，**禁止**任何 fetch / XHR / iframe；仅纯静态展示。
  - 运行时问题：A 件（Vue stories）作为 Vitest component test 装载，必须在 jsdom 中可独立 mount（不依赖 Pinia store / vue-router 全局），方便后续 e2e 复用同 props 形态。

## Acceptance Criteria

1. **AC-001 (A 件存在 + 4 状态)**：Given 仓库根目录，When 列出 `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`，Then 文件存在且 `describe("PlaygroundPrototype")` 内含 4 个 `it("renders ${state} state")`，state ∈ {`empty`, `typing`, `responded`, `error`}；每个 `it` 都 `mount()` `PlaygroundPrototype.vue` 并断言关键 DOM（如 empty 态有 `[data-testid="placeholder"]`，error 态有 `[data-testid="error-banner"]`）。
2. **AC-002 (A 件可独立 mount)**：Given Vitest 在 jsdom 中执行 AC-001 中的 `it`，When 不挂 Pinia / 不挂 vue-router / 不连后端，Then 所有 4 个 it 全绿；这保证 prototype 是"无依赖的视觉胶片"，后续 e2e 可直接复用 props/state 结构。
3. **AC-003 (B 件存在 + 单页可双击打开)**：Given 仓库根目录，When 打开 `docs/approved/attachments/playground-prototype-2026-04-23.html`，Then 该 HTML 自包含（CSS 内联，无外链 JS），双击在浏览器打开后能见到 4 个画面（垂直排列或 tab 切换皆可）；不发起任何网络请求（`<head>` 无 `<script src>`，无 `<link href="http">`，无 `<img src="http">`）。
4. **AC-004 (A↔B 视觉一致 — 状态枚举)**：Given A 与 B 都呈现 4 个画面，When 对比，Then 状态名（empty/typing/responded/error）、画面顺序、每个画面的关键文案（input placeholder、assistant 回答示例、error 文案、用量小条文案）逐字一致；通过 `docs/approved/attachments/playground-prototype-AB-parity.md` 文档列对照表显式锁定。
5. **AC-005 (B 件无外部依赖)**：Given B 件 HTML，When 用 `grep -E '(http://|https://|src=|href=")' docs/approved/attachments/playground-prototype-2026-04-23.html | grep -v '^[[:space:]]*<!--'`，Then 输出**仅**包含 `data:` URI 或纯锚点（`href="#..."`）—— 无任何 `https://` / `http://` 外链；内置 SVG / 颜色块即可代替图标。
6. **AC-006 (A 件不动主路由 / 主 sidebar)**：Given 本 PR diff，When 检查 `frontend/src/router/index.ts` 与 sidebar 组件，Then **没有**新增 `/playground` 路由、**没有**新增 sidebar 入口；prototype 不掺入主流程；后续 follow-up PR 才上路由。
7. **AC-007 (设计明确表态)**：Given prototype HTML 文件底部，When 阅读，Then 依次回答了设计文档 §11.2 列出的 5 个问题：是否允许 system prompt（首版）/ 是否显示推理过程 / multi-turn 上下文位置 / 单次最大限制（msgs/tokens/timeout）/ 移动端断点。每问一句结论 + 一句理由。
8. **AC-008 (回归 / 现有前端测试)**：Given 本 PR 落地，When 跑 `pnpm test:run`，Then 现有所有测试通过（不动现有断言）。

## Assertions

- `frontend/src/components/playground/PlaygroundPrototype.vue` 存在，导出一个接受 `props: { state: 'empty' | 'typing' | 'responded' | 'error' }` 的组件。
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts` 4 个 `it` 全绿。
- `docs/approved/attachments/playground-prototype-2026-04-23.html` 存在；`grep -c 'data-state=' docs/approved/attachments/playground-prototype-2026-04-23.html >= 4`。
- `docs/approved/attachments/playground-prototype-AB-parity.md` 存在；包含一个对照 markdown table，列 = 状态，行 = (画面要素 / Vue selector / HTML selector / 关键文案)。
- `frontend/src/router/index.ts` 与 `frontend/src/views/layout/AppSidebar.vue`（或现有 sidebar 文件）**未被本 PR 修改**（`git diff --name-only main..HEAD` 不含这两个文件）。

## Linked Tests

- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`PlaygroundPrototype`
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`renders empty state`
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`renders typing state`
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`renders responded state`
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`renders error state`
- `frontend/src/components/playground/__tests__/PlaygroundPrototype.spec.ts`::`AB parity: each Vue state has matching HTML data-state` — 用 fs 读 HTML mockup 的 `data-state` 属性，断言与 Vue 组件 `props.state` 枚举一一对应。

运行命令：

```bash
cd frontend && pnpm test:run -- PlaygroundPrototype
```

## Evidence

- A 件归档：`frontend/src/components/playground/PlaygroundPrototype.vue` + `__tests__/PlaygroundPrototype.spec.ts`
- B 件归档：`docs/approved/attachments/playground-prototype-2026-04-23.html`
- 视觉一致归档：`docs/approved/attachments/playground-prototype-AB-parity.md`
- 4 张并排截图（A vs B）作为 PR review 时的视觉证据 — 在审批人审过 prototype 后由实装 PR 归档。

## Status

- [x] InTest — A 件 `PlaygroundPrototype.vue` + 5 个 Vitest 全绿（4 state renders + AB parity gate）；B 件 `playground-prototype-2026-04-23.html` 自包含无外链（`grep -E 'https?://|src=|href="' → 0 行）；对照表 `playground-prototype-AB-parity.md` 锁定 4 状态、15 个 `data-testid` selector、8 个颜色 token 一致；未引入 `/playground` 路由 / sidebar 入口（AC-006 通过 `git diff --name-only` 核对）；5 个设计决策（system prompt / reasoning / multi-turn / per-call limits / mobile breakpoint）在 HTML 底部明确表态。等待审批人视觉过审后翻 Done → 进入 follow-up PR 实装 `PlaygroundView.vue` + 路由 + e2e。

## 实施差异说明

- **不引入 Storybook**：当前 `frontend/package.json` 没有 Storybook 依赖（实测 `pnpm list @storybook` 无结果）；为单纯展示 prototype 引入完整 Storybook 工具链会让 PR 体积膨胀（额外 ~80 个 dev deps + 一个 build target），违反 PR 单一意图原则。改用 **Vitest component test + 自包含 Vue 组件** 的形态：
  - 工程对接价值（A 件价值核心）保留：组件文件 `PlaygroundPrototype.vue` 暴露真实 `props.state`，后续实装 PR 直接复用同 props 结构 + 拓展为 `PlaygroundView.vue`。
  - 视觉过审价值（B 件价值核心）保留：静态 HTML 双击即看，与 Storybook 等价。
  - 视觉一致性约束保留：通过 `playground-prototype-AB-parity.md` 对照表 + AC-004 + AB parity test 三件锁定。
  - 这是对设计文档 §11.1 表格中 "Storybook story（必做）" 一行的等价替代——形态变化但价值约束不变。后续若团队引入 Storybook，可零成本将 `PlaygroundPrototype.vue` 包装成 `*.stories.ts`。

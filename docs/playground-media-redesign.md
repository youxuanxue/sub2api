# TokenKey 图片 / 视频 Playground + Pricing 改造设计

> 设计提案（未 approved）。乔布斯之眼：聚焦、简洁、端到端、设计即工作方式、精品意识。
> 重点在**图片/视频的生成体验**。一切价格、模型、字段均经代码核验（见文末「事实核验表」）。
> 调研：toapis.com 竞品体验 + 业界最佳 playground（fal.ai / Replicate / Krea / Sora / Runway / Midjourney / AI Studio）+ tokenkey 现状全量测绘。
>
> **事实复核：2026-06-14 对 `main` @ `b5af7714`（含 PR#766）7-agent 全量重核。** 关键漂移已回灌：**veo-3.1 $0.40→$0.60/s、veo-3.1-fast $0.15→$0.30/s（PR#766 上调）**，故 8s Veo = **$4.80**（非 $3.20）；首发 servable 仅 imagen-4×3（图片）+ veo-3.1（视频），seedance/seedream 待 probe；veo「失败不收费」已升级为官方已验；后端依赖为 4 处协同的 catalog 改动簇（非单字段）。设计骨架（计费引擎公式、异步视频骨架、误路由 bug、图片「—」bug）全部复核成立。

---

## 0. 一句话洞察（北极星）

> **「价格印在生成按钮上，结果在页面里播放出来，你永远不需要知道底层是 Vertex、火山还是 OpenAI。」**

这条北极星不是口号，它由两条**结构性事实**支撑——而这两条恰好是任何转售型竞品（toapis）无法伪造的：

1. **TokenKey 拥有自己的计费引擎。** `CalculateImageCost` / `CalculateVideoCost` + `tk_pricing_overlay.json` 在请求发出**之前**就算出确切金额（预扣 hold 就是这个数）。toapis 是转售商，只有一个不透明的 credit 计量器、事后扣费——它的**视频价格在整个页面上渲染成 `-`**。我们把 hold 这个数搬到按钮上：`生成 · $4.80`。
2. **TokenKey 拥有真实的异步任务骨架。** `vt_` task id、`VideoTaskCache`（Redis 24h TTL）、terminal-status 自动删除、`RefundVideoUsageOnFailure`（幂等全额退款）。这套东西工程账已经付过，前端今天却把它扔掉（刷新即丢任务、视频只留 1 个、失败甩一坨原始 JSON）。

**结论：我们要做的几乎全部是「在已有管道之上做呈现」，不是造能力，是掀开帘子。**

而当前产品在第一拍就违背了北极星：**没有图片/视频入口**——模态是 `modalityForModel()` 从你恰好选中的某个 model id 字符串里**猜**出来的副作用。这是最不高级的原罪，也是 Kling/Sora/Gemini-image 等模型今天**静默误路由到 `/v1/chat/completions`** 的真实 bug。第一刀就砍掉它。

---

## 1. 竞品真相：toapis 的「playground」是什么

> 修正一个早期误判：toapis **有** playground，但藏在每个 `/model-guide/<model>` 落地页里——**一个模型一张页**，不是统一工作室。

以 `toapis.com/en/model-guide/gpt-image-2` 为例，它是 **SEO 营销落地页 + 内嵌 try-it + 价格文档** 的混合体：

| toapis 做了什么 | 代价 / 弱点 |
|---|---|
| `Describe your image` prompt 框（带 `127/1200` 计数）+ 快捷 prompt | **生成被闸住**：`Generate Free` 跳登录；结果区默认是 `Example output`（示例图，非你刚生成的） |
| 宽高比选择、分辨率 `1K/2K/4K`、参考图上传 | **碎片化**：每个模型一张独立页，**无法跨模型对比**，无统一工作室 |
| `Request preview`（实时拼 API 载荷 JSON） | **无历史/画廊**，刷新即走 |
| `ToAPIs vs Official Pricing` 对比表、Key Parameters、Common Errors、FAQ | **视频面板是静态 PNG + 价格 `-`**，异步等待体验缺失（它是 SEO 页，不是创作台） |

**toapis 唯一真正强的地方**：图片 model-guide 页是个**会转化的 landing page**（参数即文档、价格对比就在手边、`$10 充值`/`免费 credits` 钩子清晰）。我们要借这个长处，用在它空着的地方（视频）。

**给 tokenkey 的直接启示**：toapis 是「每模型各自为政 + 要登录才动 + 视频价格不敢露」。tokenkey 作为多平台网关（Vertex Veo / 火山 Seedance / gpt-image 同一把 key），可以反过来做——**统一创作台 + 登录后即真生成 + 价格印在按钮 + 跨平台并排对比**。把竞品的「碎片化 + 示例图占位 + 视频 `-`」翻成我们的「统一 + 真实时 + 多模型同台 + 价格透明」。

---

## 2. 三条结构性杀手锏（竞品抄不了）

- **A — 价格在按钮上（估价即扣价）。** 真实计费引擎驱动的实时单次估价。math 已在服务端 ship，前端镜像几乎免费；而效果是对一个把视频价显示成 `-` 的对手的降维打击。
- **B — 一次 prompt，多模型同台（Bake-Off）。** 勾选 2–4 个跨厂商模型，一次「生成」，看 Veo / Seedance / Sora 并排出片，各自真实价格在面板下跳动。单一上游的工具永远做不出来。它把 tokenkey「上游多到让人困惑」的负债，翻转成「来这里的唯一理由」。
- **C — 失败全额退款，看着钱回来。** `RefundVideoUsageOnFailure` 幂等退款让我们能**承诺并兑现**「失败不收钱」。视频是最贵、最易失败的操作，这是最大的 bill-shock 恐惧，而竞品不敢承诺。

---

## 3. Playground 改造 — 图片生成体验

### 3.0 路由与文件结构（尊重 upstream-isolation 约定）

- **新建独立媒体路由 `/studio`，不动 chat。** 当前 `PlaygroundView.vue` 把 chat/image/video 三个 panel 用 `v-if` 塞进一个 823 行单文件——这是根烂。Chat 留在 `/playground`（仅修掉那句陈旧的「Requests go straight to /v1/chat/completions」副标题），媒体迁到全新 `/studio`。
- 编排器 `views/user/studio/MediaStudioView.vue`，下挂 `ImageStudio.vue` / `VideoStudio.vue` / `BakeOff.vue`。
- 所有 TK-only 纯逻辑走 `*.tk.ts`：`constants/studioMediaPresentations.tk.ts`、`composables/useTkMediaCostEstimate.ts`、`composables/useTkMediaLibrary.ts`、`composables/useTkVideoTaskPoll.ts`。
- 删 `components/playground/PlaygroundPrototype.vue`（死 mock，US-032 遗留，未路由）。

### 3.1 模态是第一个、显式的、用户拥有的决策

页面顶部两个大分段 chip：**图片 | 视频**。这**取代 `modalityForModel()` 作为真值来源**。选了模态，model picker 只显示该模态模型，于是**误路由 bug 结构性消失**：图片态只 POST `/v1/images/generations`，视频态只 POST `/v1/video/generations`。`modalityForModel` 降级为防御性 fallback。

### 3.2 模型选择 = 「质量档位卡」，不是 model id 下拉框

砍掉裸 id 的 flat `<select>`。用户选的是**任务 + 档位 + 价格**，不是「imagen-4.0-ultra 在 channel_type 41」：

| 档位卡 | 背后模型（真实可服务） | 卡上价格 | 厂商（默认隐藏） |
|---|---|---|---|
| **Draft 草稿**「快、便宜、迭代」 | `imagen-4.0-fast-generate-001`（首发）· `seedream-4-0-250828`（已定价，待 probe 点亮） | **$0.02 / 张**（imagen-fast）· $0.0299（seedream，待点亮） | via Vertex · via 火山 |
| **Standard 标准**「最佳平衡」 | `imagen-4.0-generate-001` | **$0.04 / 张** | via Vertex |
| **Ultra 极致**「最高细节」 | `imagen-4.0-ultra-generate-001` | **$0.06 / 张** | via Vertex |

**决定性取舍：**
- **档位卡为默认，「Advanced ▾」里放原始 model id 让 power user 直接选。** 默认是「档位×价格」，深度按需展开——同时拿到「人话不暴露厂商」和「专家能选具体模型」。
- **每张卡带一张「已知良好输出」示例缩略图**（随版本 ship 的静态资产）。
- **厂商是脚注，不是标题。** 卡片右下角灰字 `via Google Vertex` / `via 火山`。
- **只显示「实测可服务 AND 已定价」的模型。** 数据源 = 后端 `pricing_catalog_supported_models_tk.go` allowlist ∩ 当前 key 所在 group 的 `GET /v1/models` 池。`seedream` 随 probe 确认自动点亮。**绝不显示未定价模型**——后端 `TkImageModelUnpriced` 本就会 400 它。
- **gpt-image 不做默认、不进档位卡。** 它需要 `type=apikey` 的 openai 账号，OAuth 订阅号会静默 502。只在 Advanced 出现，且硬标「需要 apikey 类型账号」。绝不拿 footgun 当首选。

### 3.3 整体布局（图片态）

```
┌───────────────────────────────────────────────────────────────────┐
│  Studio   [ 图片 ][ 视频 ]          Key: ▾ Pro group   余额 $14.30  │
├──────────────────────┬─────────────────────────────┬──────────────┤
│  左栏 (编排)          │   画布 (结果网格)            │  右栏 成本面板 │
│  ~360px               │   缩略图网格 + lightbox      │  (设计的脊柱) │
│  • 档位卡 (Draft/..)  │   每张带「回执 chip」实付价  │              │
│  • Prompt (主英雄区)  │   hover: 下载/重掷/变体/动画化 │ 预估 $0.06   │
│  • 参考图拖拽          │   空态 = 3 张示例输出+prompt │ 余额后 $14.24 │
│  • 分辨率 chip ×倍率  │   (点击载入)                 │              │
│  • 数量 n 步进器       │   底部: 持久化历史 strip     │[生成·$0.06]  │
└──────────────────────┴─────────────────────────────┴──────────────┘
```

把当前那个 chat 形右 `<aside>`（系统提示、token 用量、连接外部应用）**从媒体态彻底删除**——那是 chat 家具。「复制 base URL / 复制 key」收进 prompt 区一个小「API」popover。

### 3.4 Prompt UX —— 永不空白，永不一墙字段

- **永不空白规则（fal.ai 最强模式）：** 选定档位卡时 prompt 预填该档位「已知良好示例 prompt」（就是缩略图背后那条）。第一次点击 = 真结果，绝不是报错。
- **Quick Prompts chip 行**（toapis 的廉价高价值细节）：*产品图 · 人像 · Logo · 风景 · 动漫*。
- **参考图拖拽区（image-to-image / edit）：** 接入当前从未在 playground 用过的 `components/common/ImageUpload.vue`，作为一等输入，穿进 `reference_images[]`（image-to-image 是 Runway 教的「更省钱的默认路径」）。
- **参数 = 视觉 chip + 渐进披露**，关闭态永远能生成：

  **分辨率 chip，把倍率印在 chip 上**（全设计最关键的定价清晰细节）：
  ```
  分辨率:  [ 1K ×1 ]   [ 2K ×1.5 ]   [ 4K ×2 ]
  ```
  倍率直接来自 `billing_service.go:getDefaultImagePrice`（已核验：`2K→×1.5`、`4K→×2`）。用户在选 4K 之前就**看见**它为什么更贵。
  - **默认 chip = 1K（显式最便宜）。** 决定性取舍：**不提供欺骗性的「Auto = 最便宜」选项。** 后端把空 size 默认按 2K（×1.5）计费（`billing_service.go:919-925`）——当前 UI 藏了这个**静默 1.5× 超收**。我们要么默认显式 1K，要么在 auto chip 旁硬写「Auto 按 2K 计费（×1.5）」。绝不让看起来便宜的选择悄悄更贵。
  - **数量 `n` 步进器 1–4**（当前完全缺失）。后端已接受 `n` 且按**实际交付张数**计费。估价线性 ×n。
  - **Advanced（再折叠，仅 gpt-image）：** quality / style / background——转发上游，明确标注「不改变价格」（后端无 quality 倍率）。

### 3.5 右栏成本面板 + 生成按钮 = 价格标签（杀手锏 A）

```
┌─────────────────────────┐
│  这次生成                 │
│  Standard · 2K · ×1.5    │
│  $0.04 × 1.5 × 1 张      │
│  ─────────────────────   │
│  预估   $0.06            │  ← 实时，任一控件变化即重算
│  余额   $14.30           │
│  生成后  $14.24          │  ← credit→具体产出 (Runway 教的)
└─────────────────────────┘
   [  生成 · $0.06  ]         ← 按钮就是价格
```

- **客户端实时计算**（`useTkMediaCostEstimate.ts`，从 catalog 喂数），翻 chip / 调 n 即刻更新，无往返。
- **服务端 hold 即真值校验**：真实请求走预扣 hold（同一个 `CalculateImageCost`），估价 → hold → 结算一个数贯穿。它们不会分歧，因为是同一公式。
- **余额不足：** 按钮 disabled，显示 `生成 · $0.06 — 充值` 链到计费，**在请求之前**拦下，镜像后端 `403 insufficient_balance`，用户不再吃莫名其妙的服务端错误。
- **真实倍率（my 视图）：** 估价应用用户真实 `rate_multiplier`（DTO 已带，当前页面丢弃）。1.5× group override 的用户看到的就是他真要付的价。

### 3.6 结果 + 历史 + 迭代闭环（图片）

画布是**缩略图网格 + lightbox**，不是 debug 竖列。每张结果 tile：
- **回执 chip：** 显示**实际结算价**（从 usage record 读，不是估价）——平台首次显示媒体实付成本。估价 $0.06、回执 $0.06，信任复利。
- **hover 暴露迭代闭环按钮**（Midjourney 教的）：
  - **下载**（当前缺的 table-stake——真 `<a download>`，`b64_json` 走 blob，不再「在新标签打开巨大 data: URI」）；
  - **重掷 Re-roll**（同 prompt+params 一键）；
  - **变体 Variations**（prompt 微调重跑）；
  - **动画化 Animate →**（把这张静图路由进视频态作 first-frame——同池路由让 still→video 衔接天然，单一上游工具做不到）；
  - **用此 prompt**（载回 prompt 区 fork）。
- **历史持久化：** 替换内存 `.slice(0,8)`（静默丢第 9 张、刷新即丢）为 **localStorage 库**，每项 `{src, prompt, model, params, cost, ts}`，容量 50 且显式（「显示最近 50 · 清空」）。v1 不做后端 gallery——localStorage 是 90% 的胜利。

---

## 4. Playground 改造 — 视频生成体验

> 视频是**对手留出的整条空道**（toapis 视频面板是一张静态 PNG + 价格 `-`）。tokenkey 把价格放出来、把视频播出来、把异步等待变得可读。

### 4.1 编排（左栏）—— 档位卡带每秒价

| 档位 | 背后模型（可服务/已定价） | 卡上费率 |
|---|---|---|
| **Fast 快** | `veo-3.1-fast-generate-001` / `doubao-seedance-2-0-fast-260128` | **$0.30/s**（Veo）· $0.119/s（Seedance-fast） |
| **Standard 标准** | `seedance-1-0-pro-250528` | **$0.109/s** |
| **Cinematic 电影级** | `veo-3.1-generate-001` | **$0.60/s** |

> 实测核验（overlay 显式字段 `output_cost_per_second`，2026-06-14 核验 b5af7714）：veo-3.1 = **$0.60/s**、veo-3.1-fast = **$0.30/s**（**两者均经 PR#766 2026-06-13 由 $0.40 / $0.15 上调**，对齐 Vertex 官方 MAX 档 4k+audio 价）；seedance-1.0-pro = $0.1088/s，seedance-2.0 = $0.3699/s，seedance-2.0-fast = $0.1194/s（这三个 #766 未动）。
> **首发可服务现实：empirical allowlist（`pricing_catalog_supported_models_tk.go`）里视频只有 `veo-3.1-generate-001` 一个实测 200**——所以**首发只有「Cinematic」一档真能点亮**，Fast / Standard（veo-fast、seedance 系）已定价但待 `tokenkey-servable-model-refresh` probe 确认后自动出现。档位卡数据驱动，绝不显示「可服务 AND 已定价」之外的任何卡。

编排其余：档位卡 → 预填 prompt + Quick Prompts（*镜头推进 · 产品旋转 · 电影感 · 自然风光 · 竖屏社交*）→ **first-frame 图拖拽**（后端只允许 `image_url` first-frame，硬拒 `video_url` 续写；dropzone 标「首帧图（可选）」，绝不提供会 400 的续写上传）→ 参数 chip：

- **时长 = 滑块，1–60s，默认 8s，带实时价**——全设计最高冲击的控件。拖动时按钮 `生成 · $4.80` 跟着跳（8s × Veo $0.60）。后端「省略默认 8s、clamp 1–60」变成一个有触感、有价格的手势。**这正是 toapis 渲染成 `-` 的东西，我们渲染成一个在你拇指下移动的数字。**
- **宽高比 chip**（16:9 / 9:16 / 1:1）——透传给 adaptor（TK 不解释，无害）。

### 4.2 成本面板（视频变体）—— 失败退款是信任线

```
┌──────────────────────────┐
│  这个视频                  │
│  Cinematic · 8s           │
│  $0.60/s × 8s             │
│  ────────────────         │
│  预估   $4.80            │
│  余额 $14.30 → $9.50     │
│                           │
│  ✓ 失败的视频全额退款       │  ← 非协商的信任线
└──────────────────────────┘
   [  生成视频 · $4.80  ]
```

「失败的视频全额退款」**对用户永远成立**：tokenkey 的 `RefundVideoUsageOnFailure` 幂等全额退款，**独立于上游是否收费**。
> 诚实标注：seedance 系「失败不收费」是**官方原文已验证**（火山 Ark「仅对成功生成的视频计费。因审核等原因导致生成失败的，不收取费用。」）；Veo 系**也已补上官方原句**——PR#766（2026-06-13）把 overlay 的 veo source note 从早先的 metric-inferred 升级为 `Official Vertex AI pricing`（cloud.google.com/vertex-ai/generative-ai/pricing，verified 2026-06-13，`failure_billing=success_only`）。对**用户**而言无差别——我们退款逻辑自管，「失败退你钱」在两个平台都兑现。

### 4.3 异步等待 —— 永不冻结 spinner（匠心时刻）

当前 UX 是一张卡 +「waited 42s」计数 +「Stop polling」链——debug 视图。替换为**可见的任务时间线**：

```
┌─ Veo 3.1 · 8s · 16:9 ──────────────── $4.80 已预留 ─┐
│  ⬤ 已提交     ✓  (vt_a1b2…)                          │
│  ⬤ 生成中     ◔  00:42                               │  ← 动画化 elapsed
│  ○ 就绪                                              │
│  ▒▒▒▒▒▒▒░░░░░░  通常 30–90s                          │  ← 诚实预期带
│  "霓虹东京小巷，慢推镜头，雨"                          │
│  $4.80 已预留 · 失败则退款                            │
│  [ 完成通知我 ]   [ 离开，稍后回来 ]                    │
└──────────────────────────────────────────────────────┘
```

底层是已存在的 `pollVideoOnce`（5s 间隔）+ 3 连续错误容忍，UI 呈现状态机 `已提交 → 生成中(elapsed) → 就绪`（或 `失败 — 已退款 $4.80`）。

- **多任务排队为卡片栈**——杀掉「视频只留 1 个、每次提交摧毁上一个」这个最糟当前 bug。提交三个，走开，回来看三个播放器。
- **刷新可存活（关键可靠性修复）：** 在飞 `vt_` 任务持久化到 localStorage（`{taskId, keyId, model, prompt, params, submittedAt}`），mount 时**重新挂接轮询**（`useTkVideoTaskPoll.ts`）。后端记录在 Redis 活 24h——我们只是不再扔掉句柄。当前刷新即永久丢任务（captured key 只在 JS 内存）。
- **通知后走开（fal/Sora 模型）：** terminal 状态触发应用内 toast +（可选）浏览器 Notification：「你的 Veo 片子好了」。30–120s 渲染没人盯着。
- **就绪：** 内联 `<video controls poster>` **在页面里播放** + 下载 + 重掷 + 复制 prompt。`extractVideoUrl` deep-scan 保留，但 success-without-URL **不再甩原始 JSON**——显示「视频好了 — 打开 ↗」带链接，原始 JSON 收进 dev-only `<details>`。
- **失败：** 人话（「生成失败 — 已退你 $4.80，试试更短的片子或别的档位」）+ 折叠的「技术详情」。**绝不把原始 JSON blob 当主 UI。**

### 4.4 视频画布

完成的视频落进与图片**同一个持久化库**，每项带回执 chip（实际结算秒数 × 费率）、poster 缩略图、下载、重掷。视频历史从「正好 1 个、被覆盖」变成真 gallery。

---

## 5. 杀手锏 B：跨平台一次 prompt 多模型对比（Bake-Off）

**一条 prompt → 多个模型并排出片，各自带实时价。** 单一上游的 playground 永远抄不了，而对 tokenkey 几乎免费——因为每个模型本就走同一个 compat 池。这一个 feature 把「上游多到困惑」翻转成「来这里的唯一理由」。

```
Prompt: "霓虹东京小巷，慢推镜头，雨"            时长 8s
┌──────────────┬───────────────┬───────────────┐
│  Veo 3.1     │  Seedance 1.0  │  Seedance 2.0  │
│  ▶ [播放器]  │  ▶ [播放器]    │  ⏳ 生成中      │
│  $4.80       │  $0.87         │  $2.96         │  ← 各自真实价 (overlay)
│  ⏱ 48s       │  ⏱ 31s         │  …            │  ← 速度+价格，买家在意的两轴
│  [选它继续]  │  [选它继续]    │              │
└──────────────┴───────────────┴───────────────┘
```

- **从档位卡的「+ 对比」复选框武装**（勾 2–4 个）。一次「生成」fan-out 成 N 个提交（图片：N 个并行 `/v1/images/generations`；视频：N 个并行 `vt_` 任务，各自独立轮询）。
- **每个面板显示自己的实时价 + elapsed/延迟**——用户用**自己的 prompt** 亲眼发现：Seedance 比 Veo 便宜 5×（$0.87 vs $4.80 / 8s）还更快，或 Veo 的运动质感值这个溢价。**用演示做成本教练**（Runway 的「草稿用便宜的、终稿用好的」，但让你**感受**到而非读到）。
- **「选它继续」** 把胜者钉回单模型流继续迭代。
- **护栏：** 最多 4 面板；视频 Bake-Off 在 fan-out 前警告「这将合计扣约 $8.63」（各实时估价之和——诚实，因为估价是真的）。

**裁决：Bake-Off 放 Phase 2。** Phase 1 先把单模型「价格在按钮 + 视频播放 + 异步可读」这条命脉跑通（最快可见胜利），Bake-Off 是放大器不是地基。

---

## 6. Pricing 改造

当前 `/pricing` 把图片渲染成字面 **「—」**（真 bug：`normalizedRows` 丢掉 `image_output_per_1k`、模板无 image 分支），且**视频概念完全不存在**（`MePricingBillingMode` 字面缺 `'video'`）。我们不把媒体硬塞进 token 表——**打破僵硬的 `NormalizedRow`，给媒体一等的、原生的单元。**

### 6.1 三个模态 tab（镜像 Studio）

顶部分段 tab：**文本 · 图片 · 视频**。当前混在一起的 flat list 是发现失败的根——用户不知道名字就找不到图片模型。Tab 免费解决分类。

### 6.2 图片定价 —— 卡片 + 尺寸阶梯可见

```
┌──────────────────────────────────────┐
│  Imagen 4.0 标准         via Vertex   │
│   1K   $0.04        ●●● 示例           │
│   2K   $0.06   (×1.5)                  │
│   4K   $0.08   (×2)                    │
│  按生成的每张图计费                     │
└──────────────────────────────────────┘
```
- **修掉丢字段 bug：** `normalizedRows` 读 `output_cost_per_image`，模板加 `image` 分支。
- 尺寸阶梯（×1/×1.5/×2 套在真实 per-image base 上）显式展示——用户第一次能回答「4K 图多少钱」。
- gpt-image（按 token 计费）给它诚实表示：「按图片 token 计费（约 1K 图 ~$X）」，不硬塞 per-image。
- 真实数：Seedream-4 $0.0299/张，Imagen-4 fast $0.02 / std $0.04 / ultra $0.06。

### 6.3 视频定价 —— 每秒 + 实算单片（对手渲染成 `-` 的那个数）

```
┌────────────────────────────────────────────┐
│  Veo 3.1 电影级            via Vertex        │
│   $0.60 / 秒                                 │
│   5 秒片 ≈ $3.00                             │  ← 买家真正在意的数
│   10 秒片 ≈ $6.00                            │
│   ✓ 失败的生成不收费                          │
└────────────────────────────────────────────┘
```
- 每秒是单位；**实算单片**（5s ≈ $3.00）是买家真正推理的对象。这正是 toapis 渲染 `-` 的地方，我们放具体数。
- 「失败不收费」线（来自 `failure_billing: success_only` 已核验 / TK 退款自管）是信任差异点。
- 真实数：Veo-3.1 $0.60/s（$4.80/8s），Veo-3.1-fast $0.30/s，Seedance-1.0-pro $0.109/s（$0.87/8s），Seedance-2.0 $0.37/s，Seedance-2.0-fast $0.119/s。

### 6.4 内联估价器 = 与 Studio 共享同一引擎

页面顶部一个交互 widget，与 Studio 成本面板**同一个 `useTkMediaCostEstimate.ts`**：

```
  我要一个 [ 视频 ▾ ]  [ 电影级 ▾ ] · [ 8 ]s · [16:9]
  ───────────────────────────────────────
  花费  $4.80    (失败则 $0)
```

一个组件（`<MediaCostEstimator>`）驱动三处：pricing 页计算器、Studio 成本面板、生成按钮标签。**报价一致性由构造保证**——pricing 页、playground、后端永不对「多少钱」分歧，因为是同一公式（直接移植 `CalculateImageCost` / `CalculateVideoCost`）。

### 6.5 估价计算位置的裁决

**客户端实时计算 + 服务端 hold 即真值校验，不新增 `/me/estimate` 端点。** 理由：hold 已用 `CalculateImageCost`/`CalculateVideoCost` 算出确切数并在请求时返回；客户端镜像同公式做即时 UI（无往返、丝滑），hold 在真实请求时校正（它们不会分歧）。多倍率从 `me/pricing-catalog` 已返回的 `rate_multiplier` 取，客户端乘进去。**少一个端点、少一个往返、少一处可漂移的真值。** 公开页（无 key、无倍率）用 list 价 + 「你的实际价格可能因 group 不同」小字。

### 6.6 公开 pricing 页 = 转化落地页

借 toapis 唯一真正的强项（图片页是个会转化的 landing page），用在它空着的视频上：
- 每模态 tab + 内联估价器（视频 tab 一个 duration 滑块，无登录实时更新「8s Veo 3.1 ≈ $4.80」）。
- **诚实锚定省钱**（有干净官方对比时 vs Vertex/火山 list）；**没有就只显示我们的数，清楚地显示**——绝不像 toapis 那样显示 `-` 丢信任，也绝不编造假折扣。
- 新用户经济学可见：免费额度、「$10 能买什么」（用真实 overlay 价算，且**只用首发可服务面**：「≈ 500 张 Imagen-4-fast 图（$0.02/张），或 ≈ 16s Veo 3.1（$0.60/s）」；seedance/seedream 随 probe 点亮后再加入「≈ 92s Seedance」一类锚点）。
- **「免费试用」按钮直接落进 Studio**，该模型预选、prompt 预填。
- **公开 catalog 必须扩展**：当前 `getPublicPricing` 纯 token 形（`pricing_catalog_tk.go:82-88` 的 `PublicCatalogPricing` 只有 `input_per_1k_tokens` 等），**结构上无法表达图片/视频**。比"加一个字段"更深的是：解析源 `catalogRichEntry`（`pricing_catalog_tk.go:94-110`）根本没 unmarshal `output_cost_per_image`/`output_cost_per_second`，且 `buildCatalogFromBytes`（:233）与 overlay 合并（:294-295）会**直接丢弃 token 价为 nil 的纯媒体模型**——这才是 guest 看不到媒体价的真根因。所以公开侧改动 = 扩 `PublicCatalogPricing` 结构 + 扩 `catalogRichEntry` 解析 + 放宽 nil-token-price skip + 改 `catalogModelFromEntry`（:335-350）mapper。这是本设计**唯一的后端依赖簇**（见 §8.3 / §10 风险 3 已更新）。

---

## 7. 明确砍掉什么（say no to 1000 things）

1. **不在 Phase 1 做 Bake-Off。** 单模型命脉先跑通；Bake-Off 是 Phase 2 放大器。
2. **不做 chat 进媒体 Studio。** Chat 留 `/playground`（仅修陈旧副标题）。「三 panel 共享一壳靠 v-if」是根烂，砍掉。
3. **不做裸 model-id 下拉为主控件。** 档位卡为默认，原始 id 仅在 Advanced。
4. **不向终端用户显示原始上游 JSON。** 仅 dev-only `<details>`。
5. **不暴露 channel_type / platform 内部。** 厂商是脚注。用户永不输入 model id、永不选 channel、永不学「seedance」这个词。
6. **不做 quality/style/background/seed/negative-prompt 为默认控件。** 它们不改 TK 价格（无 quality 倍率）。折叠进 Advanced。专注改变成本或结果的四件事：模型 / prompt / 分辨率档位 / 数量·时长。
7. **不做视频续写（`video_url`）输入。** 后端硬拒（会少收 ~2.4×）。只做 first-frame image-to-video。
8. **不做「Auto = 最便宜」尺寸幻觉。** Auto 按 2K 计费，明说。默认显式 1K。
9. **不显示 channel-type 可服务但未定价的视频卡**（Ali/Kling/Jimeng/Vidu/MiniMax/Sora）——后端会 400。operator 加 overlay 价后卡片自动出现（定价即闸门，by design）。
10. **不做创作套件：** 无 inpaint/region-edit、无 storyboard 时间线、无 upscale 流水线、无 fps 控制。tokenkey 是**网关**，不是 Photoshop。迭代闭环 = 重掷 + 变体 + 动画化 + 参考图，够了。
11. **不做后端 gallery / asset 服务（v1）。** localStorage 库是对的 90% 胜利。服务端库是 v2。
12. **不做 webhooks/callback UI、不做社区 feed。** 任务卡栈即队列；toast + 浏览器通知覆盖「走开」。
13. **不 ship 没 probe 过的模型。** allowlist 门禁 picker。
14. **不保留 `modalityForModel` 作为模态真值。** 模态是显式用户选择；classifier 仅防御 fallback。

---

## 8. 实施方案（具体改动点）

### 8.1 前端 — 新建文件（全部 TK-only，走 `*.tk.ts` / 新组件）

| 文件 | 作用 |
|---|---|
| `frontend/src/views/user/studio/MediaStudioView.vue` | 媒体 Studio 编排器：模态 tab + key 选择 + 余额读数 + 挂子组件 |
| `frontend/src/views/user/studio/ImageStudio.vue` | 图片态：档位卡 + prompt + 参考图 + 分辨率 chip + n + 成本面板 + 结果网格 |
| `frontend/src/views/user/studio/VideoStudio.vue` | 视频态：档位卡 + prompt + first-frame + 时长滑块 + 成本面板 + 任务时间线卡栈 |
| `frontend/src/views/user/studio/BakeOff.vue` | (Phase 2) 多模型同台对比 |
| `frontend/src/constants/studioMediaPresentations.tk.ts` | Studio 媒体展示/能力覆盖层：友好名、vendor label、支持参数、图片比例、视频离散时长；不承载可服务清单或价格 |
| `frontend/src/composables/useTkMediaCostEstimate.ts` | 纯函数 `estimate(modality, tier, {size,n}\|{seconds}, rateMultiplier)`，镜像 `CalculateImageCost`（`base × sizeMult(1/1.5/2) × n × mult`）/ `CalculateVideoCost`（`perSecond × seconds × mult`）。**单一估价源** |
| `frontend/src/composables/useTkMediaLibrary.ts` | localStorage 历史（图片 cap 50；在飞 `vt_` 持久化 + mount 重连） |
| `frontend/src/composables/useTkVideoTaskPoll.ts` | 封装 `pollVideoOnce` 状态机 + 5s 轮询 + 3 错误容忍 + 重连 + terminal 通知 |
| `frontend/src/components/studio/MediaCostEstimator.tk.vue` | pricing 页内联估价 widget（复用 composable） |

> **覆写 marker 现实（核验 `scripts/checks/upstream-override-marker.py`）：** marker 门禁按**路径模式**判定，不看 git 出身。豁免模式 = `*.tk.ts` / `*.tk.vue` / `useTk*.ts` / `constants/*.tk.ts` / `i18n/locales/*.ts` / `__tests__/`。所以本表里 `studioMediaPresentations.tk.ts`、`MediaCostEstimator.tk.vue`、三个 `useTk*` composable **天然豁免**（故 composable 刻意用 `useTk*` 前缀而非 `use*`）；但**新建的 `views/user/studio/*.vue` 仍被 marker 门禁**（匹配 `^frontend/src/.*\.vue$`，无 `.tk` 后缀）——需 commit 带 `upstream-touch-guarded`，或加 sentinel。另：新增 hotspot 文件可能触发 `check-registry-update-gate.py`，PR body 带 `sentinel-registry-reviewed` 或加 sentinel 锚点。

### 8.2 前端 — 改动文件

| 文件 | 改动 |
|---|---|
| `frontend/src/router/index.ts` | 新增 `/studio` route（`requiresAuth`，lazy，仿 `/playground` 条目 :206-217）。**upstream-owned**，需 marker |
| `frontend/src/components/layout/AppSidebar.vue` | **nav 入口在此，不在 router**——user items 数组（~:683，`/playground` 旁）加「Studio」。**upstream-owned**，需 marker |
| `frontend/src/views/user/PlaygroundView.vue` | 移除 image/video panel 与 `generateImage`/`submitVideo`/`pollVideoOnce` + 相关 state，瘦回 chat-only；修陈旧副标题 |
| `frontend/src/constants/playgroundMedia.tk.ts` | `modalityForModel` 降级为 fallback；`extract*`/`videoStateFromFetch` 解析器迁/复用进 Studio composable |
| `frontend/src/api/playground.ts` | `gatewayImageGenerations` 加 `n` / `reference_images`；`gatewayVideoSubmit` 加 `aspect_ratio` / first-frame `image_url`（`duration` 已有）；沿用 raw `fetch` 现有约定 |
| `frontend/src/views/PricingView.vue` | 拆 `NormalizedRow` 为按模态 shape；加 文本/图片/视频 tab；图片读 `output_cost_per_image` + 加 image 分支（修 `—` bug）；加 video 分支（每秒+实算单片）；加模态/厂商/价格区间 filter；my 视图应用真实 `rate_multiplier` |
| `frontend/src/api/me-pricing.ts` | `MePricingBillingMode` 加 `'video'`；`MePricingPrice` 加 `per_second?` |
| `frontend/src/api/pricing.ts` | `PublicCatalogModel` 加 `billing_mode?` + `output_cost_per_image?` + `output_cost_per_second?` |
| `frontend/src/i18n/locales/{en,zh}.ts` | 新增 `studio.*`（模态 tab、档位卡、成本面板、任务时间线、退款文案）；扩 `pricing.*`（video/per-image/per-second 单位、tab、filter）；修陈旧 `playground.subtitle` |
| 删 `frontend/src/components/playground/PlaygroundPrototype.vue` **+ 其 spec** `__tests__/PlaygroundPrototype.spec.ts` | 死 mock（生产零 importer，唯一 import 是它自己的 spec——删 .vue 必须同删 spec 否则套件挂） |

### 8.3 后端 — 唯一真实依赖（小、scoped）

| 文件 | 改动 | 上游隔离 |
|---|---|---|
| 公开 pricing catalog（`pricing_catalog_tk.go`）**4 处协同改** | (1) `PublicCatalogPricing`（:82-88）补 `output_cost_per_image` + `output_cost_per_second`（+ 若 guest 要区分模态再补 `billing_mode`）；(2) `catalogRichEntry`（:94-110）**必须新增** `output_cost_per_image`/`output_cost_per_second` 的 json unmarshal——否则字段恒为 0；(3) **放宽** `buildCatalogFromBytes`（:233）与 `applyCatalogOverlayPricing`（:294-295）的 nil-token-price skip，否则纯媒体模型仍被整条丢弃（这才是 guest 看不到媒体价的真根因）；(4) `catalogModelFromEntry`（:335-350）mapper 赋新字段。数据全取自已有 overlay，无新计费逻辑 | 已是 `*_tk.go`（marker 豁免） |
| me-pricing catalog（`me_pricing_catalog_tk.go`） | `MePricingPrice`（:201-209）已带 `image_output_per_1k`（:207）；**补 `per_second`（视频——两个展示 DTO 当前都没有此字段）** + `billing_mode`（已在 `MePricingModel` :191）派生 `'video'`。注意 `image_output_per_1k` 只在 channel 路径（`buildModelEntry` :787）set，account-fallback（:727-749）未 set——视频字段同理需覆盖两路径 | me-pricing 的 TK 端点内（marker 豁免） |

**不动：** `openai_images.go` / `openai_gateway_tk_video.go` / `pricing_catalog_supported_models_tk.go` / `tk_pricing_overlay.json` / billing engine——本设计是「呈现层」，这些是已有真值源，只读不改（allowlist 由 `tokenkey-servable-model-refresh` skill 维护）。前端新增 `n`/`reference_images`/`aspect_ratio`/`image_url` 参数后端**已接受**，无需后端改。

**提交前预检（marker 门禁按路径模式，已逐一核验）：** 需 `upstream-touch-guarded`（或 sentinel）的改动文件 = `PricingView.vue`、`PlaygroundView.vue`、`router/index.ts`、`AppSidebar.vue`、`api/playground.ts`、`api/me-pricing.ts`、`api/pricing.ts`、`views/user/studio/*.vue`（这些虽多为 TK 自著，但无 `.tk` 后缀，仍匹配 `^frontend/src/.*\.(vue|ts)$`）。**marker 豁免** = `studioMediaPresentations.tk.ts`、`MediaCostEstimator.tk.vue`、三个 `useTk*` composable、`i18n/locales/{en,zh}.ts`（locale 明确在 EXCLUDE 名单）。后端公开/me DTO 在 `*_tk.go` 内（豁免）。`no-web-impact` 不适用（本就动 web）。跑 `scripts/preflight.sh` 取真值。

---

## 9. 分期

**Phase 1 — 命脉（最快可见胜利，纯前端为主）：**
1. `/studio` 路由 + 显式模态 tab + scoped 路由（**杀掉误路由 bug + 补上不存在的入口**）。
2. `useTkMediaCostEstimate.ts` + 生成按钮价格 + 右栏成本面板（**杀手锏 A**，纯前端覆在已有 catalog 上）。
3. 图片态：档位卡 + 永不空白 prompt + 分辨率 chip（带倍率）+ n + 结果网格 + 下载/重掷/动画化 + localStorage 历史。
4. 视频态：档位卡 + 时长滑块带价 + **任务时间线状态机 + 刷新存活 + 完成通知 + 失败退款文案 + 视频在页内播放**（覆在已有 `vt_`/cache/refund 骨架上——**碾压 toapis 空道**）。
5. Pricing 页：三模态 tab + 修图片 `—` bug + 加视频每秒/单片 + 内联估价器。
6. 后端：公开 + me-pricing DTO 加 `video`/per-image/per-second 字段。

**Phase 2 — 放大器：**
1. **Bake-Off**（多模型同台，杀手锏 B）。
2. 真实倍率全面应用（my 视图 + 估价）。
3. 公开 pricing 转化落地页（省钱锚定 + 新用户经济学 + 「免费试用」落进 Studio）。
4. 变体 Variations + 「用此 prompt」fork。

**Phase 3 — 沉淀：**
1. 服务端「我的生成」库（替换 localStorage，跨设备）。
2. 浏览器通知精修 + 后台标签 badge。
3. 更多档位/模型随 probe 点亮（servable-refresh skill 自动驱动）。

---

## 10. 风险与依赖

1. **可服务清单收窄，首发档位卡很瘦（已核验 b5af7714）。** `pricing_catalog_supported_models_tk.go` 当前 probe-200：**图片只有 `imagen-4.0-{fast,generate,ultra}-generate-001`（3 个 imagen-4 变体），视频只有 `veo-3.1-generate-001`（1 个）**。seedance/seedream **不在** allowlist——所以首发**图片 Draft/Standard/Ultra 三档全部映射到 imagen-4**（fast $0.02 / std $0.04 / ultra $0.06，这反而是干净的三档），**视频只有 Cinematic（veo-3.1，$0.60/s）一档**。「Draft = seedream via 火山」、视频 Fast/Standard（veo-fast、seedance）全部待 `tokenkey-servable-model-refresh` probe 确认才点亮。**缓解：** allowlist 数据驱动，probe 确认即自动出现；首发 imagen×3 + veo×1 已足够成立，不阻塞设计。
2. **估价漂移风险（三处一致性）。** 客户端 `useTkMediaCostEstimate` 必须与 `CalculateImageCost`/`CalculateVideoCost` 逐位一致（尤其 size 倍率 1/1.5/2、auto→2K、按交付张数计）。**缓解：** hold 在真实请求时校验并以服务端为准；加前端单测对拍已知 case（8s×$0.60=$4.80、2K×$0.04×1.5=$0.06）。
3. **后端依赖簇比「一个字段」深（但仍 scoped 在 `*_tk.go`）。** 不只是加 DTO 字段：公开侧要 4 处协同（结构 + 解析 unmarshal + 放宽 nil-token skip + mapper，见 §8.3），且 **video per-second 在两个展示 DTO 当前都不存在**，me 侧也要补 `per_second`。全在 `*_tk.go`（marker 豁免），无新计费逻辑，但实现者别低估为「改一行」。
4. **gpt-image footgun。** 即便藏进 Advanced，若 group 无 apikey 账号仍会 502。**缓解：** Advanced 硬标「需 apikey 账号」，不进默认/档位卡。
5. **视频刷新重连依赖 Redis。** 单副本无 Redis 时 `VideoTaskCache` 用无 TTL 内存且跨副本不可见——重连在生产（有 Redis）成立，本地单副本 dev 是已知 caveat。**缓解：** 前端重连失败优雅降级为「任务可能已过期」而非崩。
6. **seedance-2.0 视频输入少收风险。** 续写 `video_url` 会少收 ~2.4×。**缓解：** 已砍掉视频续写 UI，只做 first-frame `image_url`。
7. **~~Veo failure_billing 为 metric-inferred~~（已消解）。** PR#766（2026-06-13）已把 overlay 的 veo source note 升级为 `Official Vertex AI pricing`（`failure_billing=success_only`），与 seedance 一样是官方原文已验。Veo 与 seedance 现在**同为官方已验「失败不收费」**。退款行为本就对用户始终成立（TK 退款逻辑独立于上游）——此风险项现仅作历史留痕。

---

## 附：事实核验表（均经本仓代码核验，非二手摘要）

| 断言 | 证据位置 | 状态 |
|---|---|---|
| 模态靠 `modalityForModel()` 从 model id 猜，无显式 tab | `playgroundMedia.tk.ts:19-25`、`PlaygroundView.vue:475` | ✅ |
| 前端 video classifier 只认 `seedance*`/`veo-*`，其余误路由 chat | `playgroundMedia.tk.ts:22` | ✅ |
| 图片结果内存 cap 8、刷新即丢、无下载/n/质量 | `PlaygroundView.vue:619-622, 224-236` | ✅ |
| 视频一次一个任务（submit 覆盖）、5s 轮询、无历史 | `PlaygroundView.vue:483-489, 639-737` | ✅ |
| 空 size 默认按 2K 计费（静默 ×1.5） | `billing_service.go:923-926`（`getImageUnitPrice`，注 #2539）；另有早一层 `NormalizeImageBillingTierOrDefault`（`:895`） | ✅ |
| 图片尺寸倍率 2K→×1.5、4K→×2 | `billing_service.go:996-1002`（`getDefaultImagePrice`） | ✅ |
| `CalculateImageCost` / `CalculateVideoCost` 函数位置（前端 composable 须逐位镜像） | `billing_service.go:891` / `:958`；时长 clamp 拆两处——下界 `seconds<=0→1` 在 `CalculateVideoCost`，上界 `>60→60`+默认 8 在 handler `videoRequestedSeconds`（`openai_gateway_tk_video.go:420-437`） | ✅ |
| imagen-4 std = $0.04/张（`output_cost_per_image`） | `tk_pricing_overlay.json:98-101` | ✅ |
| veo-3.1 = $0.60/s（`output_cost_per_second`，PR#766 由 $0.40 上调） | `tk_pricing_overlay.json:148`（key `:145`）；veo-3.1-fast = $0.30/s（`:134`，由 $0.15 上调） | ✅ |
| seedream-4 = $0.0299/张、seedance-1.0-pro = $0.1088/s 等（#766 未动） | `tk_pricing_overlay.json`（per-entry 推导；seedance-2.0 仅 `doubao-` 前缀键，无裸别名） | ✅ |
| seedance 失败不收费 = 官方原文已验 | overlay：「仅对成功生成的视频计费」 | ✅ |
| veo 失败不收费 = 官方已验（PR#766 升级 source note，~~原 metric-inferred~~） | overlay `veo-*` source note：`Official Vertex AI pricing`，verified 2026-06-13 | ✅ |
| TK 对用户失败全额退款（独立于上游） | `RefundVideoUsageOnFailure`（幂等） | ✅ |
| 公开 pricing DTO 纯 token 形（缺 media 单元） | `pricing_catalog_tk.go:84`（仅 `input_per_1k_tokens`）；`billing_mode` 仅在 `me_pricing_catalog_tk.go:191` | ✅ |
| Pricing 页图片渲染成 `—`（丢字段 + 无 image 分支） | `PricingView.vue:592-624`（`normalizedRows`） | ✅ |
| 视频异步骨架已存在（`vt_` + Redis 24h + 自动删 + 幂等退款 + 契约测试锁定） | `vt_`=`openai_gateway_tk_video.go:413`；`VideoTaskCache`=`video_task_registry.go:22`（Redis-primary + mem-fallback `video_task_cache.go:36`，TTL 24h `:20`）；terminal-delete `openai_gateway_tk_video.go:375`；`RefundVideoUsageOnFailure`=`openai_gateway_service_tk_video_refund.go:125`（dedup key `video-refund:<id>` + 双分录 + 3 次有界重试） | ✅ |
| 首发 servable allowlist 实况 | `pricing_catalog_supported_models_tk.go`：图片=`imagen-4.0-{fast,generate,ultra}-generate-001`、视频=`veo-3.1-generate-001`（图/视频同居 `supportedGeminiCatalogModels` 单 map；该文件由 `refresh-servable-allowlist.py` 在 `servable-allowlist:begin/end` 标记间生成，勿手改块内） | ✅ |
| `playgroundMedia.tk.ts:12-15` 头注释自称「never a silent misroute」与实际相悖（修误路由时一并改） | `playgroundMedia.tk.ts:12-15` vs `:22` 默认 `return 'chat'` | ✅ |
| 陈旧副标题在 i18n 不在 .vue（image/video 态并不走 `/v1/chat/completions`） | `en.ts:94` / `zh.ts:92`（`playground.subtitle`），渲染于 `PlaygroundView.vue:6` | ✅ |
| `PlaygroundPrototype.vue` 是未路由死 mock（生产零 importer，唯一 import=自身 spec） | 仅 `__tests__/PlaygroundPrototype.spec.ts` import | ✅ |

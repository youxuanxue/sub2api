# 模型基础定价模块：产品方案

> 基于 `01-china-model-pricing-api-research.md` 调研结论
> 日期：2026-04-13

## 一、为什么必须做这个模块

### 用户痛点

管理员添加一个 VolcEngine 账号，从上游拉取 112 个模型。接下来需要为每个模型配置基础定价才能正确计费。

**当前体验：** 手动查阅 VolcEngine 定价文档 → 逐个模型查找对应价格 → 在 sub2api 中逐条配置。112 个模型，按平均 2 分钟/个计算，需要约 4 小时。且价格频繁有版本后缀（`doubao-pro-32k-240515` vs `doubao-pro-32k-240828`），极易出错。

**期望体验：** 添加账号 → 拉取模型 → 系统自动匹配已知定价 → 管理员确认/微调 → 设置 `rate_multiplier` 折扣 → 完成。全程 5 分钟。

### 核心判断

**值得做。** 理由：

1. **痛点真实且高频** — 每次添加新平台账号、上游更新模型版本时都会触发
2. **数据可获取** — 虽无官方 API，但 OpenRouter + 社区数据 + 手动维护可覆盖 90%+ 主流模型
3. **竞品验证** — new-api 已实现 ratio sync 功能，证明市场有需求
4. **壁垒在数据不在技术** — 先建立定价数据池是关键差异化

## 二、产品定位

> **一句话：** 让 AI API 的定价像汇率一样透明 — 自动采集、统一换算、实时可查。

不做"又一个定价比较网站"，而是做**嵌入 sub2api 运营流程中的定价基础设施**：

- 管理员添加账号时自动匹配定价
- 计费系统自动引用最新基础价
- 定价变动时主动通知

## 三、架构设计

```
┌─────────────────────────────────────────────────────┐
│                   定价数据池                          │
│  Pricing Pool (结构化 JSON / DB)                     │
│                                                      │
│  model_id → {                                        │
│    provider, input_price, output_price,              │
│    cache_read_price, cache_write_price,              │
│    currency, unit (per_1M_tokens),                   │
│    source, source_url, updated_at                    │
│  }                                                   │
└────────────┬────────────────────────┬────────────────┘
             │                        │
    ┌────────▼────────┐     ┌────────▼────────┐
    │   数据采集层      │     │   数据消费层     │
    │                  │     │                  │
    │ • OpenRouter API │     │ • BillingService │
    │ • LiteLLM JSON   │     │ • 账号创建自动   │
    │ • 手动录入/CSV    │     │   匹配定价       │
    │ • 管理员覆盖      │     │ • 定价报表 UI    │
    │ • 社区 PR 贡献    │     │ • 变动通知       │
    └──────────────────┘     └──────────────────┘
```

## 四、功能分期

### Phase 1：定价数据池 MVP（2-3 周）

**目标：** 解决"112 个模型手动定价"的核心痛点。

#### 4.1 内置定价种子文件

在 `backend/resources/model-pricing/` 下维护一份**中国模型定价种子文件** `china_model_prices.json`：

```json
{
  "_meta": {
    "version": "2026-04-13",
    "currency": "USD",
    "unit": "per_token",
    "sources": ["volcengine_docs", "moonshot_docs", "deepseek_docs"]
  },
  "volcengine/doubao-seed-2.0-pro": {
    "input_cost_per_token": 0.000002,
    "output_cost_per_token": 0.000008,
    "cache_read_input_token_cost": 0.0000005,
    "max_input_tokens": 131072,
    "max_output_tokens": 16384,
    "litellm_provider": "volcengine",
    "source": "volcengine_docs_2026-03",
    "source_url": "https://www.volcengine.com/docs/82379/1544106"
  },
  "volcengine/doubao-seed-2.0-lite": {
    "input_cost_per_token": 0.0000004,
    "output_cost_per_token": 0.0000018
  }
}
```

**数据录入策略：**

- 首批覆盖 VolcEngine（Doubao 全系列）、DeepSeek、Kimi、MiniMax、GLM、Qwen 的主力模型
- 价格从各平台官方定价页面手动提取，注明来源 URL
- 支持模糊匹配（`doubao-pro-32k-`* 匹配所有日期版本后缀）

#### 4.2 智能模型名匹配

上游拉取的模型名（如 `doubao-pro-32k-240828`）需要匹配到定价条目（如 `doubao-pro-32k`）。

匹配策略（优先级从高到低）：

1. **精确匹配** — `doubao-pro-32k-240828` → 完全命中
2. **版本后缀剥离** — `doubao-pro-32k-240828` → `doubao-pro-32k` → 命中
3. **系列回退** — `doubao-pro-32k` → `doubao-pro` → 命中
4. **Provider 前缀匹配** — `volcengine/doubao-pro-32k` 格式兼容

#### 4.3 账号创建时自动匹配

当管理员添加 NewAPI 账号并拉取模型列表后：

- 系统自动从定价池匹配每个模型的基础价格
- 展示匹配结果（✅ 已匹配 / ⚠️ 模糊匹配 / ❌ 未匹配）
- 管理员可一键确认或手动调整未匹配项

#### 4.4 集成到 BillingService

在 `ModelPricingResolver` 的解析链中增加一层：

```
Channel 覆盖 → 中国模型定价池 → LiteLLM → Fallback → 未知
```

### Phase 2：可视化与管理（2-3 周）

#### 4.5 定价管理页面

管理后台新增「模型定价」页面：

```
┌─────────────────────────────────────────────────┐
│  模型定价管理                     [同步定价] [导入]│
├─────────────────────────────────────────────────┤
│ 🔍 搜索模型...          [厂商 ▾] [来源 ▾]       │
├─────┬──────────┬────────┬────────┬──────┬───────┤
│ 模型 │ 输入价格  │ 输出价格 │ 来源    │ 更新  │ 操作  │
├─────┼──────────┼────────┼────────┼──────┼───────┤
│ 🔷  │ $0.40    │ $1.60  │ 种子   │ 3/02 │ 编辑  │
│ doubao-seed-2.0-pro     │ 文件   │      │       │
├─────┼──────────┼────────┼────────┼──────┼───────┤
│ 🟢  │ $0.14    │ $0.28  │ LiteLLM│ 4/01 │ 编辑  │
│ deepseek-chat           │        │      │       │
├─────┼──────────┼────────┼────────┼──────┼───────┤
│ 🟡  │ $0.60    │ $3.00  │ Open   │ 4/10 │ 编辑  │
│ kimi-k2.5               │ Router │      │       │
└─────┴──────────┴────────┴────────┴──────┴───────┘
│ 共 342 个模型 | 已定价 298 | 未定价 44           │
└─────────────────────────────────────────────────┘
```

#### 4.6 定价对比报表

跨平台同类模型的价格对比视图：

```
┌─────────────────────────────────────────────────┐
│  同类模型价格对比                                  │
│                                                   │
│  💬 对话模型（旗舰级）     输入 $/1M   输出 $/1M  │
│  ├─ GPT-5.4               $2.00       $8.00      │
│  ├─ Claude Sonnet 4        $3.00      $15.00      │
│  ├─ Gemini 3.1 Pro         $1.25       $5.00      │
│  ├─ Doubao-Seed-2.0-Pro    $0.40       $1.60      │
│  ├─ DeepSeek-Chat          $0.14       $0.28      │
│  ├─ Kimi K2.5              $0.60       $3.00      │
│  └─ GLM-5                  $0.70       $2.80      │
│                                                   │
│  📊 价格趋势 (近6个月)                             │
│  ┌──────────────────────┐                         │
│  │  ╲___                │  ← 国产模型持续降价     │
│  │       ╲___           │                         │
│  │            ──────    │  ← 海外模型趋于稳定     │
│  └──────────────────────┘                         │
└─────────────────────────────────────────────────┘
```

### Phase 3：自动化采集（3-4 周）

#### 4.7 多源定价同步

**数据源优先级：**


| 优先级 | 数据源                     | 覆盖        | 获取方式      |
| --- | ----------------------- | --------- | --------- |
| 1   | 管理员手动覆盖                 | 任意        | UI / API  |
| 2   | 中国模型种子文件                | 国内        | 内置 + 手动更新 |
| 3   | OpenRouter `/v1/models` | 国际 + 部分国内 | 定时自动（每日）  |
| 4   | LiteLLM JSON            | 国际为主      | 定时自动（每日）  |
| 5   | new-api ratio sync 协议   | 按实例       | 管理员触发     |


#### 4.8 new-api Ratio Sync 协议兼容

实现 new-api 的 `/api/ratio_config` 和 `/api/pricing` 两种格式的**消费端**：

- 管理员可配置一个已有 new-api 实例作为上游
- 从该实例拉取 `model_ratio`、`completion_ratio`、`model_price`
- 自动转换为 sub2api 的 USD/token 格式
- 转换公式：`input_price_per_token = model_ratio × $0.002 / 1000`

#### 4.9 定价变动通知

当数据源更新检测到价格变化时：

- 在管理后台展示变动摘要
- 标记受影响的账号/组
- 管理员一键确认应用新价格

## 五、数据模型

```sql
CREATE TABLE model_pricing_pool (
    id              BIGSERIAL PRIMARY KEY,
    model_pattern   TEXT NOT NULL,           -- 模型名或通配符模式
    provider        TEXT NOT NULL,           -- volcengine, deepseek, moonshot, ...
    input_price     DECIMAL(20,12) NOT NULL, -- USD per token
    output_price    DECIMAL(20,12) NOT NULL, -- USD per token
    cache_read_price  DECIMAL(20,12),
    cache_write_price DECIMAL(20,12),
    max_input_tokens  INT,
    max_output_tokens INT,
    source          TEXT NOT NULL,           -- seed_file, openrouter, litellm, manual, newapi_sync
    source_url      TEXT,
    priority        INT DEFAULT 0,          -- 高优先级覆盖低优先级
    enabled         BOOLEAN DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (model_pattern, provider, source)
);

CREATE INDEX idx_pricing_pool_model ON model_pricing_pool (model_pattern);
CREATE INDEX idx_pricing_pool_provider ON model_pricing_pool (provider);
```

## 六、实现优先级

```
                        影响力
                         ▲
                         │
    Phase 1 MVP          │   Phase 3 自动化
    ┌─────────┐          │   ┌─────────┐
    │种子文件  │          │   │多源同步  │
    │名称匹配  │          │   │变动通知  │
    │自动关联  │          │   │ratio兼容 │
    └─────────┘          │   └─────────┘
                         │
    Phase 2 可视化        │
    ┌─────────┐          │
    │定价管理页│          │
    │对比报表  │          │
    └─────────┘          │
                         └──────────────────► 工作量
```

**推荐路径：Phase 1 → Phase 2 → Phase 3**

Phase 1 已经能解决 80% 的痛点（管理员不再需要逐个查文档配价格）。

## 七、与现有系统的集成点

### 7.1 BillingService 集成

```
现有链路：Channel 覆盖 → LiteLLM → Fallback → 报错
新增链路：Channel 覆盖 → 定价池 → LiteLLM → Fallback → 报错
                          ↑ 新增
```

在 `ModelPricingResolver.resolveBasePricing()` 中，LiteLLM 查询之前先查定价池。

### 7.2 账号创建流程集成

```
拉取模型列表 → 查定价池匹配 → 展示匹配结果 → 管理员确认
                                ↑ 新增
```

在 `fetchUpstreamModels` 返回后，前端额外调用定价池 API 获取匹配状态。

### 7.3 定价数据种子文件维护

采用 `backend/resources/model-pricing/china_model_prices.json` 格式，与现有 `model_prices_and_context_window.json` 并存：

- `model_prices_and_context_window.json` — LiteLLM 上游，覆盖国际模型
- `china_model_prices.json` — 本项目维护，覆盖国内模型
- 两者格式兼容，优先级：china > litellm

## 八、成本估算


| 阶段          | 工作量   | 产出                              |
| ----------- | ----- | ------------------------------- |
| Phase 1 MVP | 2-3 周 | 种子文件 + 模糊匹配 + BillingService 集成 |
| Phase 2 可视化 | 2-3 周 | 管理页面 + 对比报表                     |
| Phase 3 自动化 | 3-4 周 | 多源同步 + new-api 协议兼容 + 通知        |


**Phase 1 是最小可行产品**，足以解决当前 VolcEngine 112 个模型的定价问题。

## 九、风险与缓解


| 风险                | 概率  | 影响    | 缓解措施                                   |
| ----------------- | --- | ----- | -------------------------------------- |
| 种子文件数据过期          | 中   | 计费不准  | 每个条目标注来源 URL 和日期，定期人工校验；定价变动频率低（3-6个月） |
| 模型名匹配歧义           | 低   | 错误定价  | 分层匹配 + 管理员确认环节；模糊匹配标记 ⚠️ 警告            |
| 厂商定价区域差异          | 中   | 多收/少收 | 种子文件支持区域标注；默认使用国际价（USD）                |
| OpenRouter 价格≠官方价 | 高   | 偏差    | 明确标注数据源为 OpenRouter；国内模型优先用种子文件        |



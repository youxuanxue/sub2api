---
title: TokenKey 双市场首页与统一产品矩阵
status: approved
approved_by: feng (对话审批 2026-09-02)
authors: [agent]
created: 2026-09-02
revised_at: 2026-09-03
depends_on:
  - docs/approved/design-apex-domain-phase2.md
  - docs/approved/user-cold-start.md
---

# TokenKey 双市场首页与统一产品矩阵

## 0. 一句话目标

TokenKey 仍然只有一个产品、一个账户和一套 API；只由访问首页的 hostname 决定先展示哪组模型：

- `tokenkey.dev`：保持当前官网，优先展示全球模型；
- `global.tokenkey.dev`：面向海外用户，优先展示中国模型。

首批客户不是“所有海外开发者”，而是：

> 无法方便获得中国模型账户、支付和稳定 API 的海外 AI 产品开发者；首攻使用 Seedance / Seedream 构建图像、视频和创意自动化产品的团队。

两个首页之外，不引入任何市场分叉。90 天唯一北极星指标是：

```text
TTFC P90 < 3 minutes
```

TTFC（Time to First Call）从用户点击首页主 CTA 开始，到其第一次成功收到 Playground 或 API 调用响应结束。默认用 `deepseek-chat` 快速验证 Key、余额和 API 路径可用；TTFC 只衡量接入摩擦，不替代 Seedance / Seedream 真实生成验收。

## 1. 产品不变量

以下能力必须继续共用同一个 owner，不因市场、域名、语言、IP 或注册来源产生分支：

| 能力 | 唯一事实来源 |
| --- | --- |
| 用户身份 | 同一 user ID、登录态和 Session Cookie |
| 资产 | 同一 USD 余额、账本、订单、退款和使用记录 |
| 凭证 | 同一组 API Keys；任一市场创建的 Key 全局通用 |
| API | `https://api.tokenkey.dev` |
| 模型 | 同一 model ID、价格、路由、计费、风控和完整模型目录 |
| 产品页面 | 同一注册、Console、Models、Quickstart、Studio、支付、文档和法律页面 |
| 运维 | 同一发布物、后端、数据库、监控、客服与运营后台 |

“全球模型矩阵”和“中国模型矩阵”只控制首页的发现顺序与叙事，不控制用户能调用什么。所有用户始终可以调用完整模型目录。

## 2. Hostname 是唯一市场判定源

采用方案 B，不使用 IP 推断：

| 请求 | 行为 |
| --- | --- |
| `tokenkey.dev/`、`tokenkey.dev/home` | 当前首页，行为和视觉不回归 |
| `global.tokenkey.dev/`、`global.tokenkey.dev/home` | 海外首页，中国模型优先 |
| `global.tokenkey.dev/<其他页面>` | 灰度期 `302`、正式期 `301` 到 `tokenkey.dev/<原路径>`，保留 query；浏览器 fragment 不丢失 |
| `api.tokenkey.dev/*` | 保持现有机器 API 契约 |

`/` 与 `/home` 是同一个首页入口。现有 SPA 会把匿名用户的 `/` 导向 `/home`，因此二者必须同时允许在 `global` host 上呈现海外首页。

`global` host 还需同源提供首页运行所需的明确 allowlist：带 hash 的 `/assets/*`、favicon/logo/首页媒体和 `GET /api/v1/settings/public`。这些是首页依赖，不是第二套产品页面。任何新增依赖都必须显式加入 allowlist，不能把 `global` 放宽成第二个全量 Web 入口。

明确不做：

- IP 硬跳转或 GeoIP 分流；
- 可见地区切换器；
- market preference Cookie、用户市场字段或全局 Pinia market state；
- `?market=`、语言或浏览器时区作为市场来源；
- 两套前端工程、两套部署或两套账号系统。

所有面向海外用户的公开入口指向 `global.tokenkey.dev`；现有入口继续指向 `tokenkey.dev`。VPN、旅行或地区误判不会把用户困住。

## 3. 海外首页信息架构

首页只保留四个动作：

1. Hero + Proof：同一首屏展示官方 Seedance 2.5 成片，产品名和价值主张必须可见；
2. Models：Seedance、Seedream 是主角；Qwen、DeepSeek、GLM、Kimi 作为 `Also available`；
3. Verify your key：一段短 OpenAI-compatible `deepseek-chat` 示例，明确它用于 30 秒验证 Key；
4. CTA + FAQ：一个最终 CTA 和四个真实问题，不增加其他营销 section。

模型顺序固定为：

```text
Seedance -> Seedream -> Qwen -> DeepSeek -> GLM -> Kimi
```

首版英文核心文案：

```text
TokenKey
China's leading AI models. One API.

Seedance, Seedream, Qwen, DeepSeek, GLM and Kimi.
Start free. No card required.
```

试用权益文案：

```text
Start with a free trial.
No credit card required.
```

海外首页只承诺“免费试用”和“无需信用卡”，不公开展示赠金金额或 token 数量。实际试用仍通过现有的 USD 统一余额机制发放，可用于账户当前可用的任何模型，包括图像和视频模型；不创建 token wallet。首发内部配置可暂保持 `$1` 赠金，但它不是对外文案或固定产品承诺。

FAQ 只回答四个问题；答案必须与真实能力、价格和已发布产品政策一致，任一问题没有确定答案时不得正式上线：

1. Which models can I use now?
2. How does the free trial work?
3. Where is my data processed?
4. How do payments and refunds work?

首页不得出现：

- 客服或支持响应时效承诺；
- 未经证据支持的可用率、渠道数量或性能数字；
- 虚构 testimonial、客户 logo 或调用量；
- 与竞品的功能/价格对比表；
- 暗示所有模型都能消费 100 万 tokens 的文案。

## 4. 核心用户路径

```text
global 首页主 CTA
  -> https://tokenkey.dev/register?redirect=%2Fquickstart%3Fmodel%3Ddeepseek-chat%26protocol%3Dopenai
  -> 使用现有注册流程
  -> 自动获得当前配置的试用余额和首个 API Key
  -> 进入现有 Quickstart，URL 预选 deepseek-chat
  -> 复制调用或进入现有 Playground
  -> 第一次成功响应
```

约束：

- 注册、Quickstart、Playground 页面不增加海外版布局、步骤或说明层；
- DeepSeek 默认值通过现有 Quickstart 的 `model` query 预选能力传入，不创建 market-aware Quickstart；
- CTA 在跳转到 `tokenkey.dev` 时保留完整 redirect query；
- 已登录用户从任一首页进入 Console 不重新登录；
- 未登录用户访问 `global` 的 `/login`、`/register` 等路径时，按当前发布阶段用 302/301 到同路径的 `tokenkey.dev` 再继续现有流程。

首个 Key 的目标显示名为 `Default Key`。现有代码已经具备自动发 Key 能力，但代码默认名目前为 `trial`；上线时通过既有 `auto_generate_default_token_name` 配置设置为 `Default Key`，不新建第二套签发逻辑。

现有注册赠金已经由 `signup_bonus_enabled` 和 `signup_bonus_balance` 支持，首发内部配置暂定为 `$1`。实施 PR 先验证邮箱注册和 OAuth 首次注册均只赠送一次、均记入统一余额与账本；除非验证失败，不改动余额数据模型。海外首页不读取或展示该配置数值。

## 5. 前端与内容 Owner

首页 profile 解析必须是一个无副作用纯函数，例如：

```text
resolveHomepageProfile(hostname)
  global.tokenkey.dev -> china-export
  all other hosts     -> current
```

只有 `HomeView.vue` 可以消费该 profile 进行页面渲染。Router 可以调用同一 owner 的 global path redirect helper，但不得衍生第二套 profile；其他页面、store、API client 和后端业务逻辑不得读取 market profile。

建议 owner 形状：

```text
frontend/src/features/home/marketProfile.tk.ts   # host -> profile，唯一判定点
frontend/src/components/home/HomeTkLanding.tk.vue
  -> shared header / auth CTA / footer
  -> current profile sections
  -> china-export profile sections
frontend/src/i18n/tk/home.tk.ts                  # 两个 profile 的展示文案
```

这不是复制现有首页。当前 profile 必须继续渲染既有内容；海外 profile 只新增自己的内容配置和必要 section。任何两个 profile 共用的登录态、Logo、语言、主题、CTA 和 API base URL 行为都必须由共享组件承担。

当前首页的 no-regression 边界：

- 默认 host、localhost、preview host 均解析为 `current`；
- 当前首页文案、模型顺序、CTA、登录态、主题和响应式布局保持不变；
- admin `home_content` override 只覆盖 `current` profile；`china-export` 必须始终渲染版本化首页，不能被全局 override 静默替换；
- 不修改 Models、Quickstart、Studio、注册、支付和 Console 的页面结构；
- 不改变模型授权、价格、路由或可服务集合。

## 6. 真实媒体资产合同

Hero 和 Proof 禁止使用假 UI、图库视频或无法追溯来源的样片。官方 Seedance 2.5 展示页的视频可以作为首发素材，不要求它来自 TokenKey 自己完成的一次 Seedream -> Seedance 工作流。上线资产包至少包含：

| 资产 | 要求 |
| --- | --- |
| Seedance Hero | 官方 Seedance 2.5 展示视频；桌面与移动端均有可用裁切；静音自动播放、循环、可暂停 |
| Poster | 从同一官方视频派生；首帧加载前不留黑框，弱网和 reduced-motion 下可独立表达结果 |
| Provenance | 记录官方来源页、模型版本、源文件与派生文件 checksum、转码/截帧说明，以及产品负责人的使用决定 |

首发交付允许使用 MP4 视频和 JPEG poster；可以按真实兼容性或性能收益增加 WebM、WebP/AVIF，但不作为上线硬门槛。媒体必须设置稳定 `aspect-ratio`，避免加载导致页面位移，不应阻塞首屏标题和 CTA；移动端需展示结果主体而非模糊背景。

## 7. SEO 与边缘路由合同

当前 storefront SEO 的 canonical 仍指向旧的 `api.tokenkey.dev`，实施时必须修正为人类入口：

| 页面 | canonical / OG URL |
| --- | --- |
| 当前首页 | `https://tokenkey.dev/` |
| 海外首页 | `https://global.tokenkey.dev/` |

Crawler prerender 必须同时根据 request host 和 path 选择 profile；`/` 与 `/home` 输出同一 profile 的 canonical。海外首页提供独立英文 title、description、OG image 和结构化数据。两个不同市场叙事不互相 canonical，暂不使用 hreflang 把它们声明成语言翻译页。

边缘配置目标：

```text
global.tokenkey.dev {
  handle homepage paths + explicit runtime assets { reverse_proxy shared app }
  handle all other document paths {
    redir https://tokenkey.dev{uri} temporary_or_permanent_by_release_phase
  }
}
```

必须复用现有 Caddy 模板、证书、发布和回滚机制，不手改线上 Caddyfile。新增 host 后仍只有一个 app upstream。生产候选状态使用 `302`，确认 host/path/query、登录和 crawler 行为稳定后，正式上线才切 `301`；回滚时先恢复 `302`，避免继续扩大永久缓存。

## 8. 产品研发与上线门禁

代码合并、域名可访问或单次冒烟成功都不等于产品已经完成。产品负责人只按三个确定状态推进：

| 状态 | 进入条件 | 对外状态 |
| --- | --- | --- |
| 开发中 | 产品基线已冻结 | `global` 不处于公开可用状态 |
| 生产候选 | 自动化合同全绿且可以确定性回滚 | `302 + noindex` |
| 已上线 | 生产验收与发布证据全部通过 | `301`，允许索引 |

进入生产候选前必须全部通过：

1. current / china-export 两种首页均由同一发布物和唯一 profile owner 驱动；
2. `registration_enabled=true`、`pricing_catalog_public=true`，且相关关闭路径已经测试；
3. 当前配置的试用赠金和 `Default Key` 对同一新用户均只发放一次；
4. 首页媒体、模型名称、价格和 API 示例与真实产品能力一致；
5. 本文档“自动化合同”全部通过，当前首页和既有 API 无回归；
6. DNS、证书、Caddy、应用版本和功能开关均有可执行回滚步骤。

从生产候选切换为已上线前必须全部通过：

1. Seedance、Seedream、Qwen、DeepSeek、GLM、Kimi 均由统一 Key 从海外网络完成真实调用；
2. Seedance / Seedream 的输入、结果、计费和失败回退完成真实 UI 端到端验收；
3. 首页 -> 注册 -> 赠金 -> Default Key -> DeepSeek 首调完整路径达到 `TTFC P90 < 3 minutes`；
4. Session 跨子域、非首页重定向、canonical、OG 和 crawler 行为符合合同；
5. 若开放支付，购买、webhook、余额入账和退款的产品路径全部成功；
6. 本文档“发布证据”无未决项。

商业、授权和法律结论是产品上线的外部输入，不在本节展开执行流程；相关责任方未给出明确可上线结论时，产品负责人不得开放对应能力。

## 9. 海外支付产品边界

代码层继续复用现有 Stripe/Airwallex、多币种、webhook、对账和退款能力，不新建海外 payment page 或海外余额。

产品研发只负责以下支付能力：

1. `payment_enabled=false` 时，首页和现有产品页面不展示不可用的购买承诺或死入口；
2. `payment_enabled=true` 时，复用现有支付页面完成下单、支付、webhook、余额入账、订单查询和退款；
3. 同一账户、余额和账本在两个市场完全一致，不按 hostname 创建支付分支；
4. 支付成功、失败、取消、重复 webhook 和退款均有自动化测试与用户可见结果；
5. 生产候选版本必须完成一次真实小额购买到退款的产品验收。

商业主体、渠道 KYC、上游授权及法律结论不属于本产品研发规格。相关责任方未给出明确可用结论时，产品负责人保持 `payment_enabled=false`，不自行补充商务流程或页面承诺。

## 10. TTFC 离线观察

不为 TTFC 新增埋点、分析 SDK、漏斗页或 onboarding 状态机。仅用人工计时和现有请求日志观察。

统一口径：

- 对象：首次接触 TokenKey、符合首批客户定义的海外目标用户；
- 起点：点击 `global` 首页主 CTA；
- 终点：Playground 或用户自己的 API 请求第一次成功返回；
- 默认任务：使用预选的 `deepseek-chat` 验证 Key、余额和 OpenAI-compatible API 路径；
- 3 分钟内未成功：按该次失败记录，不删样本；
- 成功请求模型、协议、耗时和失败原因从现有日志离线核对；
- 每周只汇总 P50、P90、成功率和前三个阻塞点，不做实时 dashboard。

TTFC 达标只说明接入路径足够简单，不说明 Seedance / Seedream 产品承诺已经兑现。媒体模型完整生成由上线门禁负责。观察只用于发现现有路径的问题；没有反复出现的证据，不增加页面、步骤或引导。

## 11. 产品负责人端到端交付

产品负责人对从产品定义到正式上线的完整产品结果负责，不在“需求交付研发”或“代码已经部署”处结束。全过程只维护一份发布检查单和一组可复核证据，并持有范围、依赖顺序和最终 Go/No-Go 结论。商业、授权和法律事项只作为外部结论输入，不进入本交付链的执行清单。

### 阶段 1：冻结产品基线

1. 以本文档固定首批客户、首页承诺、两个 hostname、完整用户路径和明确不做项；
2. 确认首页展示的六个模型具有真实可服务来源、准确 model ID 和价格；
3. 确认官方 Seedance 2.5 素材的来源、版本、checksum、派生过程和产品负责人使用决定均已记录；
4. 记录产品研发依赖的外部结论及其状态；未获得明确结论的能力保持关闭，不由研发代码推断。

完成标准：研发无需再猜测目标用户、页面结构、默认模型、域名行为或发布边界；任何新增需求先从首版删除，除非它阻断首次成功调用或已开放的核心产品路径。

### 阶段 2：完成产品研发

1. 实现唯一的 hostname profile owner，以及 current / china-export 两种首页内容；
2. 完成官方 Seedance 2.5 首屏、模型展示、DeepSeek Key 验证和 CTA；
3. 完成 `global.tokenkey.dev` 的 Caddy、证书、静态资源 allowlist、非首页重定向和回滚配置；
4. 完成双 host 的 canonical、OG、crawler prerender 和生产候选状态的 `noindex`；
5. 复用现有注册、统一余额、API Key、Quickstart、Studio 和支付页面，不产生市场分支；
6. 配置并验证试用赠金、`Default Key` 和 Quickstart 的 `deepseek-chat` 预选；
7. 补齐本文档“自动化合同”中的单元、集成和 Playwright 测试。

完成标准：实现 PR 绑定本文档，聚焦测试全绿；desktop/mobile 真实 UI 无重叠、空白媒体或当前首页回归；部署物仍然只有一份。

### 阶段 3：完成预发布验收

1. 在与生产一致的候选版本上跑完整测试、构建和项目 preflight；
2. 从海外网络分别实调 Seedance、Seedream、Qwen、DeepSeek、GLM、Kimi，核对结果、错误和实际扣费；
3. 用真实浏览器走通首页 -> 注册 -> 试用余额 -> `Default Key` -> DeepSeek 首调 -> Seedance / Seedream 生成；
4. 验证 Session 跨子域有效，非首页 path/query 正确回到 `tokenkey.dev`；
5. 演练关闭注册、catalog、赠金、支付和海外首页入口的回滚路径；
6. 汇总未关闭问题；任何影响注册、调用、计费、回滚或当前用户的缺陷均阻断生产候选部署。

完成标准：第 8 节“进入生产候选前必须全部通过”的门禁全绿，并具备确定性部署与回滚步骤。

### 阶段 4：部署生产候选版本

严格按以下顺序执行：

```text
发布同一应用版本
  -> 配置 global.tokenkey.dev DNS 与证书
  -> 启用 global host（302 + noindex）
  -> 按发布配置设置 registration / public catalog / trial bonus
  -> 执行双 host、登录、首调、媒体生成冒烟
  -> 核对错误日志、响应与实际扣费
```

任一步失败立即停止后续切换并按演练路径回滚。不得手改线上 Caddyfile；生产候选保持 `302 + noindex`，不能提前切换为已上线状态。

完成标准：生产环境可以执行完整产品验收；当前用户、API 客户端和 `tokenkey.dev` 首页不受影响。

### 阶段 5：完成生产环境产品验收

1. 使用全新测试账户走通首页、注册、试用余额、`Default Key`、DeepSeek 首调和 Seedance / Seedream 生成；
2. 分别实调六个首页模型，核对模型 ID、协议、结果、错误处理和实际扣费；
3. 验证跨子域 Session、非首页 path/query 重定向、canonical、OG、crawler 和 `noindex`；
4. 验证 current 首页、现有 Console、模型目录和既有 API 调用无回归；
5. 若 `payment_enabled=true`，完成真实小额购买、webhook、余额入账和退款产品路径；
6. 按第 10 节口径完成 TTFC 验收，修复阻断核心路径的问题后重新验证；
7. 执行一次完整回滚，再重新部署同一候选版本，证明上线过程可重复。

完成标准：第 8 节“从生产候选切换为已上线前必须全部通过”的产品门禁全绿，自动化结果与生产行为一致。

### 阶段 6：正式上线

上线当日只执行已经验证过的切换：

1. 再次检查六个首页模型、价格、余额扣费和支付状态；
2. 部署最终官网，保持 `noindex + 302`；
3. 完成一次匿名注册、DeepSeek 首调和 Seedance / Seedream 生成；若支付已开启，同时完成真实小额支付冒烟；
4. 冒烟通过后移除 `noindex`、开启英文 SEO，并将非首页重定向由 `302` 切为 `301`；
5. 检查 `global.tokenkey.dev` 的首页、公开元数据和所有 CTA 均指向正式产品路径，记录上线版本和时间。

任一核心冒烟失败，立即恢复 `noindex + 302`，关闭受影响开关并回滚应用版本。产品负责人记录唯一上线结论，不以“部分可用”宣布成功。

完成标准：海外用户从发现、注册、首次调用到媒体生成形成完整体验；支付开启时购买路径同样完整；当前市场和所有既有 API 调用无回归。

### 阶段 7：发布稳定性验收

1. 上线后 24 小时复核注册、首次调用、模型错误、余额、支付和退款产品路径；
2. 在第 7 天再次执行六模型可服务性、首页路径和当前市场回归检查；
3. 对上线暴露的阻断问题完成修复、测试、部署和回归，不在本轮加入新的市场分叉或增长功能；
4. 将最终产品状态、已知限制、测试证据、部署版本和回滚点归档到同一发布记录；
5. 90 天仍以 `TTFC P90 < 3 minutes` 检验首次使用体验，不增加产品埋点或新流程。

不以 PV、注册数、赠金领取数或模型卡点击率替代 TTFC。它们可以观察，但不成为本轮产品目标。

## 12. 验收合同

### 自动化合同

实现 PR 至少自动覆盖：

1. `tokenkey.dev` 和未知 host 解析为 current profile；
2. `global.tokenkey.dev` 解析为 china-export profile；
3. profile 只影响首页，不能改变模型权限、API Key 或余额；
4. `global /`、`global /home` 成功，非首页 document 在灰度期 302、正式期 301，且 path/query 保真；
5. current 首页 snapshot/关键文案/CTA 不回归；
6. overseas 首页模型顺序、DeepSeek API 示例和免费试用文案正确，且不展示金额或 token 数量；
7. crawler 在两个 host 得到不同且正确的 canonical/OG；
8. 已登录 Session 在子域间有效，logout 能清除 parent-domain Cookie；
9. 新用户仅获得一次当前配置的试用赠金和一个 `Default Key`；
10. 真实浏览器从海外首页 CTA 经注册到 Quickstart，模型预选为 `deepseek-chat`，页面明确这是 Key 验证；
11. desktop/mobile 下视频非空、主体可见、文本不重叠、reduced-motion 有 poster fallback；
12. `home_content` 只覆盖 current profile，不能替换 china-export；
13. Caddy 配置可确定性渲染，并能从 301 恢复为 302 后回退上线前版本；
14. 未通过上线门禁时，crawler 得到 `noindex`。

UI 端到端验收必须由 Playwright 驱动真实浏览器。后端 handler 测试、curl 或直接 API 调用不能替代第 10-11 条。

### 发布证据

以下属于带真实凭证、海外网络和生产状态的发布门禁证据，不要求在普通 PR CI 中伪造：

1. Seedance / Seedream 从海外网络完成真实生成，结果和扣费正确；
2. 六个首页模型在生产候选验收和正式上线前均通过真实可服务性检查；
3. 真实新用户只获得一次当前配置的试用赠金和一个 `Default Key`；
4. 支付开启时，真实小额购买、webhook、余额入账和退款产品路径通过；
5. 双 host、跨子域 Session、SEO 元数据和非首页重定向在生产环境符合合同；
6. 生产候选保持 `noindex + 302`，正式上线切换和回滚均已实测。

## 13. 明确延期

- 模型滚动 alias、版本固定和静默变化策略；
- 商业主体选择和上游授权的具体方案由业务另行决策，其结论作为外部上线输入；
- 新的风控或试用额度防滥用产品机制（当前只离线观察）；
- 地区自动识别、显式切换器和市场偏好记忆；
- 海外专属注册、Console、支付、文档、客服或运维体系；
- SeasAGI 的本地 MITM、Token Market 或其他功能复刻。

## 14. 乔布斯式完成定义

海外图像/视频产品开发者看到官方 Seedance 2.5 的真实结果，立刻理解“一个 Key 调中国领先模型”；点一次 CTA 后用 DeepSeek 快速验证 Key，并能继续完成真实媒体生成和付费。

现有用户完全感知不到这次架构扩展；产品研发、部署和上线始终只有一套系统。

如果实现需要解释“你现在在哪个市场”、让用户选地区、复制页面或复制账户逻辑，说明实现已经偏离本设计。如果产品显示支付可用却无法完成购买，说明产品尚未完成，不得标记为已上线。

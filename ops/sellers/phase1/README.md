# 第一期 seller 接入准备

整理为 WIP：2026-09-11。范围：NanoGPT、Poe、Hugging Face Inference Providers、EUrouter。
代码基线：2026-09-11 重新 fetch 并 fast-forward 至 `origin/main` / `a2931cc6f664187935917e6e0faab115e3babd14`。
调研、报价和生产观测仍为 2026-09-08 的时间点证据；本次归档没有重新访问生产、发邮件或刷新报价。正式外发及联调前须复核。
本目录是内部申请工作包，状态为 **材料准备中**，不是已获准入或已上线的声明。

## 已确定的账号与正式报价口径

2026-09-08 用户已确定：四家复用现有 OpenRouter billing user，通过不同 API key 标识平台，不另建四个 billing user。账号 ID 是稳定关联，不依赖邮箱名称；可选改名 `seller@tokenkey.dev` 不影响此方案，本轮保留原账号名称。

- 当前 billing user：`32 / openrouter@tokenkey.dev`。
- 每个平台使用独立命名的 universal key：`nanogpt`、`poe`、`huggingface`、`eurouter`；原 `openrouter` key 保留。
- 同账号意味着共用余额、授权分组和用户级约束，平台归因使用 `api_key_id`；不能把它描述为独立信用额度或独立用户并发额度。
- 正式报价由公开 `/api/v1/public/pricing` 与现有 seller 目录交集计算。有效倍率遵循实际计费 resolver：有用户专属倍率用专属值，否则用分组默认值，二者不相乘。
- 本次读取的授权分组为 `1/claude`、`2/GPT专线`、`19/china`，默认倍率均为 1，用户专属倍率均为 0.5。报价为公开价的 0.5 倍；模型或倍率调整后重新导出，不维护手工价表。
- OR catalog 有部分历史/回退价格型号不在当前 public pricing；这些型号保留在来源快照和排除说明中，不当作符合本次“公开价”规则的正式报价。
- 同步主干 #2050 后，导出额外读取 `ops/pricing/model-surface-bundle.json`：account override 把请求 ID 映射到另一个已公开型号时，暂停该别名的报价，避免以旧 ID 单价覆盖实际 served model 结算。2026-09-08 12:43 UTC / 20:43 北京时间快照中 `qwen-plus/max/turbo` 因此排除。此检查是潜在映射风险筛选，不代表已读全量线上账号映射或完成逐模型账单验收。

生成结果：[正式报价表](generated/seller-quote.md)。完整报价 `generated/seller-offers.json` 与内部无密钥快照 `generated/seller-snapshot.json` 含内部账号及倍率依据，仅本地生成、已忽略，不纳入公开 PR 或外发附件。新 checkout 可用下述导出命令重新取得；离线重绘需有已保存快照。模型出处、容量、平台资格和区域证明仍须完成，已确定价格不代表全部平台都可上架。

```bash
python3 ops/sellers/export_offers.py --output-dir ops/sellers/phase1/generated
python3 ops/sellers/manage_keys.py  # 只读计划
python3 ops/sellers/manage_keys.py --apply  # 获授权时创建缺失 key；不改现有 key/账号/分组/倍率
python3 -m unittest discover -s ops/sellers -p 'test_*.py'
```

导出脚本通过既有 SSM owner 读取来源，密钥仅在服务器内存用于鉴权，不进入输出。金额用 Decimal 换算，保留缓存写入、thinking、上下文阶梯和公开峰谷价。未来若各分组有效倍率不同或启用分组峰谷，脚本会拒绝推测模型对应价格，需先增加模型到实际选中分组的归因。输出是时间点报价，不是独立于线上价格调整的固定期限合同。

代码核对注意：当前 OR catalog 的 `openRouterProviderBaseMultiplier` 会再乘一次 group default；本次 defaults 全为 1，因此不影响本次基价核对。若将来改分组默认倍率，应先修正该处与 runtime override 语义的差异。公开 pricing 的部分计价维度也尚未全部暴露在 OR catalog，生成报价保留 public 来源条件，不能把它当作完成平台计费联调的证明。

## 工作包与当前状态

- [peng-zhu-handoff.md](peng-zhu-handoff.md)：给 Peng Zhu 的内部同步入口，包含必读材料、已定口径、待办及商务/工程分工；尚未实际发送给本人。
- [application-drafts.md](application-drafts.md)：四家的首封英文资格沟通稿和 EUrouter 表单预填。NanoGPT/Poe 已核实收件邮箱；HF/EUrouter 尚待确认具体收件人或经官方联系入口沟通。四封均未发送。
- [nanogpt-readiness.md](nanogpt-readiness.md)：NanoGPT 首轮容量建议、线上证据、收款选项和交付缺口；联系人为 Peng Zhu / COO。
- [offer-sheet.template.json](offer-sheet.template.json)：逐模型商业报价、容量、来源、地域及验证证据表。`null` 表示未知，不能解释为零价、无限量或无保留。
- [poe-bot.template.json](poe-bot.template.json)：待补全的私有 API Bot 请求体，无真实密钥，不可原样提交。
- [hf-model-mapping.template.json](hf-model-mapping.template.json)：待补全的 staging 模型映射，不可原样提交。

| 平台 | 已准备 | 尚缺的关键条件 | 首个可验收里程碑 |
| --- | --- | --- | --- |
| NanoGPT | 官方需求、Peng Zhu 联系人、可发资格询问稿、正式报价、专用 key、首轮容量估算及收款现状 | 对方准入答复；实际收款配置；key 评估预算；持续容量及供给出处验收 | 对方确认评估供给范围并取得专用 key 的联调反馈 |
| Poe | 最新 API Bot 契约、私有配置模板、变现条款、商务稿 | 创作者账号与实际居民资格、Stripe/税务、API Bot 收益公式 | 私有 bot 经真实 Poe UI 完成一次对话，usage 与 earnings 可对账 |
| HF | 接入步骤、账单契约、staging 映射、SDK/文档交付清单 | 公司组织/Team 或 Enterprise、商务开通、准确 Hub 映射、账单接口 | HF staging 验证通过，并能按响应 ID 收回最终费用 |
| EUrouter | 当前表单全部业务字段、真实预填、地域缺口 | 总部及合规状态；对方是否接受当前非 EU-only 供给；若要求 EU-only，再补全链路证明 | 对方接受的供给路径完成推理与账单验证 |

已读取官方页面及 TokenKey 公共政策，并审阅相关路由/计费代码。平台申请、外部消息、平台账号注册、密钥交付和付费推理均未执行。专用 key 的创建状态以 `manage_keys.py` 返回及最新内部快照为准。

### 下一步所缺材料与负责人

| 待补项 | 谁来提供 / 完成 | 影响 |
| --- | --- | --- |
| 公司现有收款渠道及开户主体 | Peng Zhu / 财务，先确认有无公司银行、Stripe/Airwallex 或企业钱包；账号走私密渠道 | NanoGPT 付费启用；其余平台按自己的采购/支付条款确认 |
| UEN/ACRA、签字授权及税务/KYC 材料 | 公司负责人 / 财务 | 对方要求时用于合同和开户；不是 NanoGPT 首封询问的公开强制附件 |
| Poe 账号、负责人真实居住地区及可参与变现资格 | 公司指定运营负责人 | 创建私有 bot、收益开通和 Stripe Payment Agent；不能拿 TokenKey key 代替 Poe 管理 key |
| HF organization URL、管理员及组织权限 | 公司指定 HF 管理员 | 准入联系、服务端开通和 staging；先确认资格再付费买 Team/Enterprise |
| 模型供给来源、允许转售的合同依据、版本及实际处理地域 | 采购/供给负责人提供证据，Agent 整理逐模型表 | 四家均需先明确，尤其闭源模型不能靠成功调用证明分销权 |
| EUrouter 总部、GDPR/DPA/SOC 2 的真实状态 | 公司/合规负责人 | 必填表单；当前按非 EU-only、非全站 ZDR 如实沟通，不预设必须迁移到 EU |
| 试用金额/期限、收款与账期、失败及缓存对账约定 | 公司商务与平台 | 交付 key 前落实；NanoGPT 的 100 RPM / 1M TPM / 50 并发仅适用于其评估，不给另外三家自动复制独占容量 |
| HF 唯一账单 ID、批量最终费用接口、目录/SDK 适配 | Agent 工程准备，HF 确认契约 | 技术缺口，由工程承担；不要求用户手工编写接口材料 |
| 外发与平台回复 | 已备稿；授权外发后经可用邮件/平台账号发送，再跟进对方 | 目前无邮件发送工具，未发送；首封资格询问不必等待全部上线材料齐备 |

## 已有公司材料

下表来自 2026-09-07 生效的公开 Privacy/Terms，2026-09-08 再次访问确认。政策声明不替代公司注册证明或运行时审计。

| 字段 | 已知值 / 材料 |
| --- | --- |
| 品牌 | TokenKey |
| 运营主体 | ORBIT LOGIC PTE. LTD. |
| 注册国家 | Singapore；实际总部/运营地址如与注册地址不同应另填 |
| 注册地址 | 60 PAYA LEBAR ROAD, #04-16, PAYA LEBAR SQUARE, SINGAPORE 409051 |
| 联系邮箱 | contact@orbitlogic.dev（现行公开支持、隐私、账单邮箱） |
| 联系人 | Peng Zhu，COO of TokenKey；用户于 2026-09-08 更新 |
| 网站 / API base | https://tokenkey.dev / https://api.tokenkey.dev/v1 |
| Privacy / Terms | https://tokenkey.dev/privacy / https://tokenkey.dev/terms |
| Status | https://status.tokenkey.dev；本次可打开，仅监控 health 不证明模型级 SLA |
| Logo | https://tokenkey.dev/logo.png；本次已确认公开可加载的 512x512 TK 标识，源为 `frontend/public/logo.png` |
| 服务描述 | AI API gateway；路由第三方模型请求，应用账号/分组策略，计量与计费 |
| 数据训练 | TokenKey 不以客户内容训练，也不通过服务关系授权上游训练；上游自身条款仍适用 |
| Prompt 日志 | 不启用全平台完整 prompt-audit；错误路径可能保留截断请求片段，最多 30 天 |
| 其他保留 | 详细计量 90 天；ops/error logs 30 天；账单为账号存续期 + 至少 24 个月或法定更长期间 |
| 当前公开地域 | 网关 US/SG；上游实际处理地域须逐模型确认 |
| SLA | 公开 Terms 不承诺具体可用性百分比，除非另有书面 SLA |

法务源文件：`deploy/aws/stage0/legal-page/privacy.html`、`terms.html`。后续变更以该 owner 和线上政策为准，不在此修改对外承诺。

仍需提供：UEN/ACRA business profile、签字授权人、实际开户/收款资料、Poe 账号及负责人真实居住资格、HF 组织及管理员。COO 联系身份不自动证明签字授权。证件、银行账号、税表和密钥通过正式私密流程交付，不放本目录。未核实 SOC 2 或 DPA 状态，不能把“未找到”填成“已认证”或“in progress”。

## NanoGPT：申请材料与交付

官方入口 [For Providers](https://docs.nano-gpt.com/api-reference/miscellaneous/for-providers)，`support@nano-gpt.com` 或官方 Discord。官方偏好联系渠道依次为 Discord、email、Twitter，不是在线自助上架承诺。

| 官方要求 | 当前准备 / 待补 |
| --- | --- |
| Company name | 品牌及新加坡运营主体已填 |
| How to get in touch | Peng Zhu / COO、公开邮箱已填；使用 email，Discord 可选 |
| Why add you / low prices / unique models | 已按公开价和当前 billing user 倍率生成报价；补充容量或目录缺口证据，未宣称全网最低价 |
| Volume discounts | 使用现有用户正式价；额外量阶折扣/最低量承诺尚未约定 |
| Special operation, e.g. decentralized | 如实描述 third-party upstream gateway；不称自营 GPU 或模型训练方 |
| Rate limits | 按用户指示提高 10 倍：100 RPM / 1M TPM / 50 并发，两小时受监控评估；输出另限 200k TPM（含于总量），独立限额未配置，持续供给未验证，见 readiness |
| Privacy / prompt logs / retention | 已有政策及准确日志摘要；不能写 no logging |
| Card / crypto / automatic payments | 线上 payment_enabled=false、无支付实例；均未启用。先商议公司预付发票及转账，实际银行受益人待确认 |
| OpenAI-compatible endpoint | `https://api.tokenkey.dev/v1`；逐模型确认 SSE、tools、usage、context |

建议首批按性价比模型和旗舰模型各选有供给证据的代表，优先检查 DeepSeek、GLM、Kimi 及 GPT/Claude 的可持续商业来源。它们是调查方向，不是已批准销售清单。不要直接将 OpenRouter 的全部目录、零售价或容量示例当成 NanoGPT 报价。

商务需问清：是否采购聚合商、允许的来源、想补的模型、试跑验收、实际批发价、阶梯折扣、付款/账期、失败与重试扣款、缓存计价、是否有最低量。公开文档没有统一 supplier 抽佣或 SLA。

## Poe：账号、Bot 与收款分开准备

官方 [API Bots](https://creator.poe.com/docs/api-bots/overview) / [Bots REST API](https://creator.poe.com/docs/api-bots/bots-rest-api) 已直接支持 Chat Completions 和 Responses，无需默认建设 Server Bot 服务。

| 材料 | 具体内容 |
| --- | --- |
| 账号与资格 | Poe 账号、负责人真实居住地区及成年资格、Creator Monetization 加入状态。Singapore 在可用地区内，但新加坡公司注册本身不能证明操作者居民资格 |
| 收款 | Stripe Payment Agent 可收款账号、真实主体/税务信息、适用 KYC/银行信息。不要预选税表类型；以 onboarding 对实际主体的要求为准 |
| 管理认证 | `https://poe.com/api/keys` 的 Poe API key；与交给 Poe 调用 TokenKey 的 key 是两种凭证 |
| Bot 展示 | handle（可用性待查）、description、头像及展示权；相关推荐、头像、私有分享需网站配置 |
| 推理 | `base_url`、精确 `model_name`、专用 TokenKey key、`api_type` |
| 能力 | 输入 `text/image/video`；video 只适用 Chat Completions。当前 API Bot 输出仅 `text`。`tools` 只在实际验证后标注 |
| 限额 | `max_input_tokens`、`context_size`；不能继承全局默认 200K 当模型实测值 |
| 价格 | `prompt`、`completion`、可选 `input_cache_reads`，均为 **USD / 单 token 的十进制字符串**；长上下文 `context_pricing.tiers` 下界含、上界不含 |
| 收益 | 用户的 pricing/compute points 与创作者 earnings 分开确认；REST API 的 bot settings 没有证明自动同额分成 |

管理契约：`POST https://api.poe.com/bots` 会创建或更新同 handle 的 bot；`PATCH /bots/{handle}` 为已有 bot 局部更新；`PUT` 整体替换设置。先核查账号已有 handle，模板明确 `is_private: true`。

报价单位转换：offer sheet 和 HF 的 USD/百万 tokens，除以 1,000,000 才是 Poe 的 USD/token；例如 $1/百万 tokens 对应字符串 `"0.000001"`。这是单位示例，不是 TokenKey 报价。实际生成配置应用十进制运算，并与长上下文/缓存维度分别核对。

变现条款已读取 [2026-08-21 Earnings Terms](https://poe.com/earnings_tos) 与 [FAQ](https://help.poe.com/hc/en-us/articles/21921312368020-Poe-Creator-Monetization-FAQs)：

- 加入计划后 90 天内必须建立可收款的 Payment Agent 账号并提供必要资料；逾期有收入没收后果，不宜在资料不齐时启动计时。
- 达 $10 后通常于月末后 30-45 天付款，处理可能另需最多 10 天；支付手续费、退款、拒付、税务或异常流量可影响实际收入。
- 估算收入不等于最终 payout；内部测试不得伪造用户互动或刷收益。
- 标准计划不适合 API 供给时可联系 `developers@poe.com`，确认企业合作与 token 级推理成本回收。

上线验收经真实 Poe UI：正常流式、工具调用（如申报）、取消/异常、最终 usage、缓存/长上下文计价、创作者 dashboard 收益及后续真实 payout。不能仅以本地 curl 返回 200 宣称 Poe 已接入。

## HF：正式 provider 交付清单

官方 [Register as an Inference Provider](https://huggingface.co/docs/inference-providers/register-as-a-provider) 要求先通过 Hub 或社交联系团队；未公开统一申请邮箱，不猜地址。

| 交付物 | 内容 / 前置条件 |
| --- | --- |
| 组织与商务 | 公司 HF organization、Write 权限管理员；模型映射阶段要求 Team/Enterprise 且 HF 服务端开通。先取得准入反馈再购买组织套餐 |
| 品牌 | server-side registration 的 SVG icon 待补；文档侧 light/dark PNG，命名 `{provider}-light.png`、`{provider}-dark.png`。已核验 `frontend/public/logo.png` 为 TokenKey。`frontend/src/utils/branding.ts` 明确 `/logo.svg` 仍是上游 Sub2API 素材，不提交为 TokenKey SVG |
| 模型表 | `hfModel` = 真实 Hub repo；`providerModel` = 专用 key 能调用的精确模型 ID；模型版本/量化/上下文/许可证和供给来源证据 |
| Task | text-generation / image-text-to-text 用 `conversational`；其他任务匹配 pipeline_tag，适配 task 级 API，不能按模型堆不同协议 |
| JS SDK | `huggingface.js` provider helper、`getProviderHelper.ts` 注册、types provider list、README、测试；conversational 可复用 BaseConversationalTask |
| Model Mapping | `POST /api/partners/{provider}/models`，从 `staging` 开始；staging 仅 partner org 成员可见，后续通过验证再 live |
| Billing | 最终账单批量查询 + 全响应唯一 ID header，详见下节 |
| Models endpoint | `/v1/models` 含 `pricing.input/output`（USD / 百万 tokens）、`context_length`；与 OR schema 2.4 的 modality pricing 不同 |
| Python SDK | Hub 工作后再接 `huggingface_hub` provider、注册表、client 文档及测试 |
| 文档 | 自己的调用/定价/隐私说明；`hub-docs` provider handlebars、partners table、toctree、generate.ts/provider URLs 及生成结果 |

验证规则：正常每 6 小时自动验证、失败约每小时重试；更改 mapping status 会立即触发。Conversational streaming TTFT <5 秒，其他任务 <30 秒，并测 tools、structured outputs。这是探测阈值，不是我们已达到的生产 SLA。

### HF 账单契约及现有缺口

请求：`POST {待实现并与 HF 确认的账单 URL}`，认证方式与 inference 一致，例如 Bearer。Body：

```json
{"requestIds":["example-request-id"]}
```

响应：

```json
{"requests":[{"requestId":"example-request-id","costNanoUsd":100}]}
```

- 每次响应（含 streaming）需要唯一响应 header，向 HF 登记 header 名；官方建议 `Inference-Id`，并非必须该名称。
- `costNanoUsd` 为非负整数，1 USD = 1,000,000,000 nano-USD；0 是确定免费，不是“还没算好”。未知项省略，无数据允许 `requests: null`。
- 每分钟查询，单批最多 10,000 ID；同 ID 反复查询应得到同一最终费用；约 30 分钟仍不能计费则 HF 不向用户收费。
- HF 仅查询 HTTP 2xx/3xx 的成功请求；SSE 已提交 200 后失败、取消和部分输出的费用规则必须与 HF 对齐。
- 查询只能返回当前供给账户有权读取的已请求 ID；金额应来自最终卖方应收，不是上游成本或任意前台标价。

代码检查：`backend/internal/server/middleware/request_logger.go` 会接受格式合法的客户端 `X-Request-ID`；不能直接把这个 correlation ID 当成不可重复的账单 ID。上游 header 转发也可能影响响应 ID。已有 `usage_log.go`、`usage_billing.go` 提供请求/账号/key 与金额字段，但此次搜索未找到 HF `requestIds`/`costNanoUsd` 路由。实现前要明确最终 ID、租户范围、最终金额与重试/取消关系，不直接暴露通用 usage 表。

## EUrouter：提交条件及材料

官方 [申请表](https://www.eurouter.ai/providers/apply) 全文字段与预填见 application-drafts。表单允许 EU-only 回答 `For some models` 或 `No`，基础设施允许 third-party，因此不能称“非 EU 主体绝对无法申请”；是否接受我们仍由对方判断。

当前材料不能证明 EU-only。公开政策写 US/SG，不能将 EU 出口、UK 节点或上游 endpoint 域名当成 EU 处理证明。所需附件：

- 每个申报模型的请求流向：入口、网关、实际推理、日志/缓存、备份、支持访问及分包商处理地域。
- 上游合同/数据驻留条款及实际部署证据；区域故障时不得静默 fallback 到非 EU 的配置与验证。
- 是否保留 prompt/response、保留期限、ZDR 的具体适用模型/计划。当前不是全站 ZDR。
- DPA 可签署文本及主体、subprocessor 清单、跨境处理安排；GDPR 自我声明的依据；SOC 2 如有报告需核实范围和有效期。
- 每 key RPM/并发、日/月容量及持续容量证据；折扣、模型差异、正式结算条款。

缺乏上述证据时，可先用真实事实开展资格沟通；不能为提交必填表单而编造 Yes/No 或未经决策承诺迁移。

## 工程准备与验收边界

| 现有 owner | 可复用内容 | 本期要验证/补充 |
| --- | --- | --- |
| `backend/internal/server/routes/gateway.go` | Chat Completions、Responses | 新平台专用 key 下的模型 ID、stream/tools/usage；路由存在不等于逐模型可用 |
| `backend/internal/server/middleware/api_key_auth.go` | 现有 API key 鉴权和账号策略 | 按用户已定方案共用 billing user 32，独立 API key 做平台归因；不共享同一原始 key |
| `backend/internal/service/openrouter_provider_tk_catalog.go` | OR 目录构建及模型元数据线索 | OR 格式不能原样用于 HF；普通 key 与 OR billing-user key 的 `/v1/models` 行为有差别 |
| `backend/internal/service/usage_billing.go` / `usage_log.go` | 现有最终结算/用量 | HF 外部对账所需 ID 与金额语义、持久化时机、租户约束 |
| `ops/pricing/probe-openrouter-provider-inference-serial.py` | 逐模型探测方式参考 | OR 专用探测不能当四家平台联调证据；模板容量不作生产承诺 |

后续验收材料统一记录：平台、模型和 key 标识（无 secret）、时间、版本、调用地域、样本量、输入/输出长度、并发、p50/p95 TTFT、吞吐、成功率、缓存命中、HTTP/SSE 结果、usage、平台账单和我们的实际应收。容量按持续测试/实际运行证据填写，首 token 心跳不能当模型正文 TTFT。

公开申请材料只包含对外模型/报价和验证汇总；原始上游账号、成本底价、内部组号和原始请求日志不直接作为附件。

推进顺序：补齐主体负责人/平台账号和容量事实 -> 对方确认准入与商业条款 -> 专用凭证与适配实现 -> staging/私有 UI 联调 -> 审核上架 -> 小量真实交易与回款。HF 账单/鉴权及 EU 路由承诺属于后续需明确契约的实现，当前没有修改公共接口。

## 来源与证据口径

本次主要来源均于 2026-09-08 读取官方全文：

- NanoGPT: https://docs.nano-gpt.com/api-reference/miscellaneous/for-providers
- Poe API Bots: https://creator.poe.com/docs/api-bots/overview
- Poe REST: https://creator.poe.com/docs/api-bots/bots-rest-api
- Poe Creator: https://creator.poe.com/docs/resources/creator-monetization
- Poe Costs: https://creator.poe.com/docs/resources/how-we-cover-your-costs
- Poe FAQ: https://help.poe.com/hc/en-us/articles/21921312368020-Poe-Creator-Monetization-FAQs
- Poe Earnings Terms（页面更新时间 2026-08-21）: https://poe.com/earnings_tos
- HF: https://huggingface.co/docs/inference-providers/register-as-a-provider
- EUrouter: https://www.eurouter.ai/providers/apply
- TokenKey: https://tokenkey.dev/privacy / https://tokenkey.dev/terms / https://status.tokenkey.dev

平台官方要求、TokenKey 公开声明和本工作包建议分别标注；没有公开供应合同的项目继续标待确认。网站文档和表单可能更新，实际提交前复核。

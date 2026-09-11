# Seller 申请与资格沟通稿

内部草稿，2026-09-08。没有发送。方括号内的待补项需取得事实后替换；未决商业条件不能默认接受。共享事实来源、验收与技术材料见 [README](README.md)。

## NanoGPT

To: support@nano-gpt.com

Subject: TokenKey provider inquiry - model supply, pricing and capacity

Hello NanoGPT team,

We operate TokenKey, an AI API gateway run by ORBIT LOGIC PTE. LTD. in Singapore. We would like to explore supplying inference to NanoGPT through our OpenAI-compatible endpoint.

Our service routes requests to third-party model providers and meters usage. Could you confirm whether you onboard this type of supplier, and which model families or capacity gaps you are currently prioritizing?

Provider details:

- Company: ORBIT LOGIC PTE. LTD.; service: TokenKey.
- Website: https://tokenkey.dev
- API base URL: https://api.tokenkey.dev/v1
- Contact: Peng Zhu, COO of TokenKey; contact@orbitlogic.dev. Email is our preferred contact for this inquiry.
- Evaluation candidates: GPT-5.6 Luna, Claude Sonnet 5 and DeepSeek V4 Pro, subject to your model priorities and confirmation of permitted supply scope. These have recent successful production usage; the dedicated NanoGPT integration has not yet been validated.
- Proposed models and input/output/cache rates: see the attached TokenKey Seller Quote. Context and time-window pricing are included where listed. Additional volume discounts and minimum commitments have not been agreed. The broader quote is a selection list; launch availability will be confirmed per model.
- Proposed initial evaluation envelope, aggregate across models: 100 requests/minute, 1,000,000 total tokens/minute (including cached input and output), no more than 200,000 output tokens/minute within that total, and 50 concurrent requests. We propose a monitored two-hour evaluation with no burst above these limits. Please pace requests on your side during evaluation; these are proposed operating limits, not independently enforced or reserved capacity on the key today. Long requests remain subject to the same token and concurrency limits. Continuous production capacity and any SLA will be agreed after evaluation.
- Supply arrangement: gateway using third-party upstream services; source and permitted distribution scope will be documented for the proposed models.
- Privacy: https://tokenkey.dev/privacy. We do not maintain a platform-wide full prompt archive. Error paths may retain truncated request excerpts for up to 30 days; detailed usage metering is retained for 90 days. We do not use customer content to train models. Upstream terms and policies also apply.
- Terms: https://tokenkey.dev/terms
- Status: https://status.tokenkey.dev
- Payments: USD-denominated usage pricing. Our online checkout is currently disabled; credit card, crypto and automatic recharge are not currently enabled. We propose discussing prepaid business invoicing and bank transfer, subject to confirming the beneficiary account and your acceptance. If card or crypto is required, please share your preferred method so that we can assess setup before paid traffic. No payment details are included in this email.

Please also share your evaluation criteria, expected capacity, purchasing and payment terms, treatment of cache usage and failed requests, and the secure process for providing a dedicated evaluation key. A dedicated key has been provisioned; we will finalize its evaluation spending limit before secure delivery. We can finalize the proposed supply and commercial terms against your requirements.

Regards,
Peng Zhu
COO, TokenKey
TokenKey | ORBIT LOGIC PTE. LTD.
contact@orbitlogic.dev

附件使用 [正式报价表](generated/seller-quote.md)，不外发内部 snapshot/JSON。以上 NanoGPT 正文已无占位符，可用于首封资格及评估询问，尚未发送。容量依据、付款选项和正式上线前缺口见 [NanoGPT readiness](nanogpt-readiness.md)。邮件未承诺赠送额度、赊账、已完成付款设置或保底 SLA。

## Poe

To: developers@poe.com

Subject: TokenKey API Bot supply and monetization onboarding

Hello Poe developer team,

TokenKey is an AI API gateway operated by ORBIT LOGIC PTE. LTD., a Singapore company. We are preparing API Bots backed by our Chat Completions endpoint at https://api.tokenkey.dev/v1 and would like to clarify the supplier and monetization setup before onboarding.

We have reviewed the API Bot settings, per-token pricing and Creator Monetization documentation. Could you clarify:

1. Whether a company operating a gateway to third-party inference providers can participate, and the source/authorization documents you require.
2. The eligible account and payment setup for our company and its authorized operator, including any residence requirements independent of company incorporation.
3. For API Bots, how api_bot_settings.pricing maps to creator earnings and inference-cost reimbursement. Is an additional earnings setting or a custom commercial arrangement required?
4. How cached tokens, long-context tiers, interrupted streams and partial responses affect payable earnings, and which reports support reconciliation.
5. Whether you prioritize additional suppliers for existing models or models not yet available. Our initial candidates include GPT-5.6 Luna, Claude Sonnet 5 and DeepSeek V4 Pro, subject to permitted supply scope and your requirements. We can share our prepared USD token quote, including the applicable cache and context conditions.

Our website is https://tokenkey.dev, privacy policy is https://tokenkey.dev/privacy, and terms are https://tokenkey.dev/terms. We do not use customer content for model training; our policy discloses limited error-path request retention and third-party processing.

We are confirming the company operator's account and monetization eligibility. The initial evaluation bot would be private, with text output and only verified capabilities enabled; its handle will be selected after checking availability.

Please advise the appropriate onboarding path and secure method for exchanging evaluation credentials.

Regards,
Peng Zhu
COO, TokenKey
TokenKey | ORBIT LOGIC PTE. LTD.
contact@orbitlogic.dev

以上正文可用于首封资格询问，尚未发送。配套：`poe-bot.template.json`。填写模型/handle/价格/密钥前，先确认真实主体资格和收益配置；不能因配置了 pricing 就认为已经建立付费供应关系。

## Hugging Face

渠道：官方指引为 Hub 或社交联系；以下是拟发送给实际确认的 Inference Providers 团队联系人的正文，未指定或猜测收件地址。

Subject: TokenKey - Inference Provider eligibility and integration proposal

Hello Hugging Face Inference Providers team,

We would like to discuss onboarding TokenKey as an Inference Provider. TokenKey is an AI API gateway operated by ORBIT LOGIC PTE. LTD. in Singapore, using third-party upstream inference services.

Our OpenAI-compatible base URL is https://api.tokenkey.dev/v1. We propose starting with conversational models that can be mapped to exact Hub repositories, subject to confirmation of the model version, deployment details and permitted supply scope.

Before completing the integration, could you confirm whether this supplier model is eligible and what commercial, source-verification and billing requirements apply?

Preparation status:

- Company Hub organization and authorized administrator: being confirmed. Please advise when the Team/Enterprise requirement applies in your onboarding process.
- Initial mapping: pending verification of exact Hub repositories, upstream model identity, version, quantization, supported context and permitted supply scope. We will propose only models with a verified mapping.
- JS integration: planned against BaseConversationalTask, with provider registration and task compatibility tests.
- Billing: we are preparing unique response IDs and an authenticated bulk cost lookup returning final integer costNanoUsd. This endpoint is not yet available for evaluation.
- Catalog: we will provide pricing.input/output in USD per million tokens and context_length in the HF-compatible models response.
- Assets: our current TokenKey PNG is available at https://tokenkey.dev/logo.png. A provider SVG and the documentation light/dark assets remain to be prepared.
- Documentation and Python SDK integration: planned following the Hub staging integration.

Please confirm the server-side enablement process, staging test setup, request ID header agreement, billing timing, supplier settlement terms and the secure credential exchange process.

Website: https://tokenkey.dev
Privacy: https://tokenkey.dev/privacy
Terms: https://tokenkey.dev/terms
Status: https://status.tokenkey.dev

Regards,
Peng Zhu
COO, TokenKey
TokenKey | ORBIT LOGIC PTE. LTD.
contact@orbitlogic.dev

以上正文可用于首轮资格沟通，实际联系人渠道尚待确认，未发送。配套：`hf-model-mapping.template.json`。不将 closed-model 商品名编造为 Hub repo；组织套餐未购买，SDK PR 未提交，账单 URL 未对外宣称可用。

## EUrouter

渠道：官方 Contact 入口；供应商申请入口为 https://www.eurouter.ai/providers/apply 。首封询问的具体收件邮箱尚未核实，不猜测地址。以下正文可用于邮件或官方联系表单，尚未发送。

Subject: TokenKey - provider eligibility and regional processing requirements

Hello EUrouter team,

I am Peng Zhu, COO of TokenKey, an AI API gateway operated by ORBIT LOGIC PTE. LTD., a Singapore company. We would like to confirm our eligibility to supply inference through EUrouter before completing your provider application.

We route requests to third-party upstream providers through an OpenAI-compatible endpoint at https://api.tokenkey.dev/v1. We have prepared a USD model quote covering input, output and cache pricing, with context and time-window conditions where applicable. Proposed launch models and capacity would be agreed after confirming your supply requirements and the permitted distribution scope.

Our public policy describes gateway processing in the United States and Singapore. We cannot currently guarantee EU-only processing or platform-wide zero data retention. Actual upstream inference and other processing locations need to be verified per proposed model. TokenKey does not use customer content for model training. Error paths may retain truncated request excerpts for up to 30 days, and detailed usage metadata is retained for 90 days; upstream policies also apply.

Could you clarify:

1. Whether you consider Singapore-incorporated gateway suppliers using third-party infrastructure, including suppliers that cannot currently guarantee EU-only processing.
2. Whether EU-only processing is required for every listed model or can be evaluated per model, and which evidence you require for inference, logs, backups, subprocessors and regional failover.
3. Which DPA, GDPR, security assurance and source-authorization materials are mandatory for initial qualification and for production onboarding. We are confirming our documentation and are not making a certification claim in this inquiry.
4. Which model families or price and capacity gaps you are prioritizing, and your evaluation criteria, provider purchasing terms, settlement methods and billing reconciliation requirements.

Website: https://tokenkey.dev
Privacy: https://tokenkey.dev/privacy
Terms: https://tokenkey.dev/terms
Contact: contact@orbitlogic.dev

Please advise the appropriate next step and secure process for exchanging evaluation credentials and any non-public company or supply documents.

Regards,
Peng Zhu
COO, TokenKey
TokenKey | ORBIT LOGIC PTE. LTD.
contact@orbitlogic.dev

以上正文无待填占位符，可用于首封资格沟通。未将 NanoGPT 的评估额度另行承诺给 EUrouter；完整申请表仍需补齐下列事实。

### EUrouter 表单预填

入口：https://www.eurouter.ai/providers/apply 。本表是人工可复核材料，不是直接发送的 API payload。`待补` 不是表单选项；提交前需有真实答案。表单最后还有反垃圾字段 `Website`，不是第二个公司网址，不填写。

| 表单原字段 | 拟填 / 处理 |
| --- | --- |
| Company name * | ORBIT LOGIC PTE. LTD. (TokenKey) |
| Company website * | https://tokenkey.dev |
| Contact person * | Peng Zhu |
| Role * | COO, TokenKey |
| Email * | contact@orbitlogic.dev |
| Headquarters location * | [实际总部待确认；已知注册国家为 Singapore] |
| Company logo URL | https://tokenkey.dev/logo.png （已核验可公开加载的 TokenKey PNG） |
| In which region(s) do you run inference? * | Our public policy describes gateway processing in the United States and Singapore. Actual upstream inference regions require confirmation per model. [补逐模型实际推理地域] |
| Can you guarantee EU-only processing? * | 当前公开服务不能作该保证；若按现状申请选 No。只有具备已验证 EU 模型后才可选 For some models，并附模型/地域证据 |
| Do your AI models run on hardware you own and operate? * | Third-party infrastructure；TokenKey 为网关，非自营模型硬件声明 |
| Rate limit per minute | [每 API key RPM 待实测/定约] |
| Max concurrent requests | [每 API key 并发待实测/定约] |
| Max requests per day | [待定] |
| Max requests per month | [待定] |
| Anything else about your capacity? | [TPM、模型差异、持续/突发窗口、拥塞响应、扩大容量提前期] |
| Do you use customer data for training? * | No，就 TokenKey 自身政策；备注说明上游自身条款仍适用并补核实 |
| Do you retain prompts/responses? * | Yes，保守披露错误路径截断请求片段；不是完整成功请求档案 |
| If yes, for how long? | Limited error-path request excerpts: up to 30 days. Detailed usage metadata: 90 days. See our privacy policy for accounting and other retention periods. |
| Do you offer Zero Data Retention? * | 当前不能声明 ZDR；按现状选 No。后续仅对已核实模型/计划作专项承诺 |
| GDPR compliant? * | [依据和负责人员确认后选择；现有 Privacy 页面不等于完成合规核验] |
| SOC 2 certified? * | [尚未取得认证状态；不能自行填 Yes 或 In progress] |
| Do you have a DPA ready to sign? * | [尚未取得可签 DPA；不能自行填 Yes 或 In progress] |
| Privacy policy URL | https://tokenkey.dev/privacy |
| Terms of service URL | https://tokenkey.dev/terms |
| Willing to offer a discount to EUrouter end-users? * | [商务待定；Open to discussion 也需有真实意向] |
| If yes, what discount could you offer? | [基准价格、折扣、适用模型、量阶、有效期待定] |
| What makes your offering stand out? * | TokenKey provides a unified API gateway with request routing, account/group policies, usage metering and billing. [补具体模型、实测容量或价格优势，不写未经验证的欧洲/低延迟保证] |
| Anything else we should know? | We are a Singapore-incorporated gateway operator using third-party upstream providers. Our current public service is not represented as EU-only. Please advise whether you would consider a specifically documented EU model supply arrangement and which residency and DPA evidence you require. |

若先开展资格询问，可经其公开 Contact 页面联系，正文应明确当前不能提供 EU-only 保证。完整申请前必须补齐必填项，不能静默改造成全欧洲供应商。

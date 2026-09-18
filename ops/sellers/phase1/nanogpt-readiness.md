# NanoGPT 首轮接入准备

内部工作材料，2026-09-08。联系人：**Peng Zhu, COO of TokenKey**。运营主体：ORBIT LOGIC PTE. LTD.。当前已准备 [英文资格询问稿](application-drafts.md#nanogpt)、[37 模型正式报价](generated/seller-quote.md) 和专用 key；未发邮件、未提交凭证、未完成对方验收。

## 容量建议

2026-09-08 用户指示将原建议容量提高 10 倍，以下为更新后的**评估流量上限**。两小时评估窗口及单请求长度保持原口径；此调整是用户确定的目标，不是新压测结果、已配置的 key 限流或可签约保底 SLA。

| 指标 | 首轮建议 | 口径 |
| --- | --- | --- |
| RPM | 100 | 所有模型合计，均匀发起请求，不额外允许 burst |
| TPM | 1,000,000 | 输入（含缓存读/写）+ 输出；输出另限 200,000 TPM，包含于总量，不额外相加 |
| 并发 | 50 | 所有模型合计；长输出/推理占槽期间降低实际 RPM |
| 持续窗口 | 2 小时受监控评估 | 按上述上限理论最多 12,000 请求、120M 总 tokens、24M 输出 tokens；这是流量上界，不是压测结果或已授权的付费调用预算 |
| 工作负载 | 平均每请求 <=10k 总 tokens，初轮最多 32k 输入及 2k 输出 | 任一分钟全部限额同时满足；100 个 32k 输入请求不在 1M TPM 范围内。推理 tokens 按模型实际 usage 计入输出 |
| 首批候选 | gpt-5.6-luna、claude-sonnet-5、deepseek-v4-pro | 在报价表内且近期有成功调用；来源许可、对方认可及专用 key 推理验证仍待完成 |

建议均衡分布约 GPT 40 RPM、Claude 20 RPM、DeepSeek 40 RPM；这是评估 mix，不是另加三份额度。不要用分组总吞吐证明一个单模型可全部承接。

与已有观测对照：授权分组 24h 平均合计约 40.12 RPM；最近完整 60 分钟约 47.68 RPM。新目标 100 RPM 约为这两个已承载量的 2.49/2.10 倍。最近一小时未缓存输入 + 输出约 654k TPM，新目标 1M 总 TPM 约为其 1.53 倍；缓存占比不同，不能用这个比值证明容量已验证。按近期分组均值 11.45-33.60 秒，100 RPM 需要的平均在途量约 19.08-56.00；在较慢 workload 下，50 并发会先触顶，应降至约 89.3 RPM 或更低。P95 达 77 秒，不能保证全部请求同时跑满 RPM/TPM。50 并发相当于 seller 共享账号配置上限 100 的一半，尚未预留独占份额。

两小时后再进行至少 24h 混合模型观察并覆盖峰时，后续扩量按结果另定。每日/月度保底、24x7 恒定吞吐和 99.9% SLA 目前无足够证据；不把 100 RPM 直接外推成每天 144,000 次供货承诺。

## 线上证据

只读快照：2026-09-08 12:33:09 UTC / 20:33:09 Asia/Shanghai。统一落稳截止：12:28 UTC / 20:28 北京时间；60m 窗口为 11:28-12:28 UTC，24h 为前一日 12:28 至当日 12:28 UTC。

| 授权分组 | 24h 请求 | 平均 RPM | 计量 TPM（含缓存） | 60m 平均 RPM | 60m 计量 TPM | 60m mean / P95 秒 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Claude / 1 | 6,259 | 4.35 | 468,338 | 6.68 | 288,799 | 20.19 / 45.26 |
| OpenAI / 2 | 30,023 | 20.85 | 1,565,815 | 29.02 | 2,325,305 | 11.45 / 41.86 |
| China / 19 | 21,492 | 14.93 | 848,623 | 11.98 | 299,500 | 33.60 / 76.76 |

- TPM 来自 usage_logs 的 input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens；不重复加缓存 5m/1h 子项。按计量日志写入分钟归桶，是**成功计量的吞吐**，不是请求到达 TPM、实时执行吞吐或限流器口径。失败请求不在这些使用量统计中。
- 60m 全站另一个稍晚窗口 11:29:18-12:29:18 UTC 完成 4,589 次（76.48 RPM），最高分钟 145；包含非 seller 授权分组，不能全用于此 seller。分组的各自峰值不相加。
- 24h seller 自身只有 1 条计量记录，不能声称已经验证 NanoGPT key 的持续负载。候选模型在授权分组的 24h 请求：GPT-5.6 Luna 3,775；Claude Sonnet 5 1,608；DeepSeek V4 Pro 1,059。
- user 32 active，配置 concurrency=100，user/group rpm_limit=0，有可支用余额。0 表示该配置不限制，不表示供给无限。所有 marketplace keys 共用用户余额/并发。
- NanoGPT key 548 active，quota=0、5h/1d/7d 金额限额均 0、无到期日：目前没有独立支出上限。不得将拟定 RPM/TPM/并发称为已配置；交付前需完成评估金额预算、期限及请求节流安排。不在邮件正文附 key，不提供共享账号登录。
- 24h SLA probe：96,153 条使用量、2,293 条平台/上游归类错误、44,866 条客户端归类错误。工具显示 98.400%，其分母含客户端错误；按成功/(成功+平台/上游错误) 粗算约 97.67%。两者均为日志诊断口径（存在流中失败、事件重复及归类限制），不能用作合同可用性。其中 China 路由有 119 条 no-available-accounts 429，进一步支持先小流量验证。
- 本轮 7 天全 Edge 持续无错并发收集在 us3 因 access 留存不覆盖窗口而拒绝，未产出完整新报告。没有绕过留存检查。仓库 2026-07-20 旧报告只能作历史背景，不能代表当前余量。未把 prod 镜像账号与 Edge 原始账号容量相加。

可重复命令（原始输出含内部运营信息，留本地 .cache 或 /tmp）：

```bash
bash ops/observability/run-probe.sh --target prod --script ops/sellers/probe-readiness.sh
bash ops/observability/run-probe.sh --target prod --script ops/observability/probe-fleet-traffic-window.sh --env WINDOW_MINUTES=60
bash ops/observability/run-probe.sh --target prod --script ops/observability/probe-sla-breakdown.sh --env WINDOW_HOURS=24
python3 ops/observability/edge_capacity_report.py collect --edges auto --days 7 --min-seconds 60 --raw-dir .cache/nanogpt-capacity-raw --output .cache/nanogpt-edge-capacity.md --timeout-seconds 120
```

## 收款选择

[NanoGPT 官方 For Providers](https://docs.nano-gpt.com/api-reference/miscellaneous/for-providers)（2026-09-08 核实）问供应商是否支持 credit card、crypto、automatic payments；没有公开规定供应商必须使用某一结算通道、稳定币网络或付款周期。NanoGPT 自身给用户充值的支付选项不等于其向供应商付款的承诺。

生产快照：payment_enabled=false；payment_provider_instances 无记录；ENABLED_PAYMENT_TYPES 为空。只能确认站内支付未开启，不能推断公司在站外没有银行/Stripe/Airwallex 账户。

| 方案 | 适用性与建议 | 启用前还缺什么 |
| --- | --- | --- |
| 公司银行转账 + USD 发票 | **首选协商**，试期预付、充值到账后消费；对方是否接受待回复 | 现有公司开户主体、可收 USD 路径、银行/中间行费用、入账对账方式；不假定已有 ACH/SWIFT/本地美元收款能力 |
| 信用卡，Stripe 或 Airwallex | NanoGPT 官方明确询问；代码有集成，但线上无配置。也可评估合法主体的站外 Payment Link/Invoice | 已获批商户账户、币种、实际费率、3DS/拒付和结算安排；代码支持不等于商户可收款 |
| USDC / USDT 企业钱包 | 官方询问 crypto，币种/网络由双方确认；作为备选，不默认 NanoGPT 支持任何指定网络 | 企业控制的钱包、明确 coin/network、收款主体/发票、确认数与金额规则；地址通过私密渠道提供 |
| 自动充值/自动扣款 | 是付款自动化方式，须依托卡/银行/其他授权支付；不是独立币种或必备准入条件 | 付款授权、余额阈值/单次及累计上限、幂等扣款和失败通知、完整对账。当前未启用 |

建议首封邮件提出“USD 计价 + 小额预付 + 公司发票/转账，听取对方偏好”；不承诺 Net30、赠送余额、额外折扣或自动扣款。收款方式、到账周期、是否预付和平台采购价是独立商业条款。正式单价沿用生成报价，不通过支付通道额外修改模型倍率。

## 推进顺序与缺口

1. 审阅并授权发送已完成的 NanoGPT 首封询问稿至 support@nano-gpt.com。当前会话无邮件发送工具，且尚无明确外发授权；未发送。
2. 等对方确认接受第三方聚合供给、意向模型、付款方式及评估流程。官方未要求首封邮件附所有 KYC 文件，UEN/ACRA 与开户证明可在需要时走私密渠道。
3. 商定实际收款账户、评估支出上限/期限及双方节流配置；现有独立 key 仅解决归因，不能隔离全部风险。无需给对方 seller 共享登录。
4. 为获选模型补齐实际上游与允许转售的合同/授权、精确模型身份及数据处理地域；当前流量成功不证明供给权利。报价中的旧模型有过不支持/缺价错误，不能整表直接上线。
5. 通过安全渠道交付 key，完成真实 NanoGPT 流程的 SSE、usage/缓存/峰谷与长上下文费用、异常/取消对账及两小时容量验收。当前只查目录和日志，未进行付费推理或平台验收。

首次资格沟通可以现在发；正式计费供给仍需上述事实与对方确认。联系人、报价规则和首轮容量估算已经补齐。

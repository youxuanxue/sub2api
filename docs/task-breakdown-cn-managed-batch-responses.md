---
status: draft
scope: local-implementation-and-tests
---

# 北京站国内账号的托管 Batch Responses 执行（本地修复）

目标：对 direct Key 和 universal Key，若同一账号及其冻结的 Plan 可执行非流式 Responses 请求，托管 Batch 支持经审核的国内供给路径。首验 china / glm-5.3-flash。不是按模型名硬编码，也不是开放所有供应商。

证据：2026-11-24 11:30 UTC，北京站 vol-test 的十个样本均在 provider 门禁失败；错误文件与任务 ID 见 `biaoshu/tender/service-evidence-20261124/batch-glm-vol-test-01/`。错误中的空字符串来自账号平台到 Batch provider 的映射，不能反推具体账号 ID 或实际 Plan。

本地授权范围：2026-11-24，用户批准本地修复与测试，并明确要求同时支持 direct 与 universal；不含发布或生产配置写入。每条请求只能使用当前 Key 已获授权的路径；结算绑定本次选中的分组和订阅。

实施顺序：先从单次受控选择取得脱敏账号平台、适配器和目标协议，不采集正文或凭据；核实在线 Responses 的现有执行 owner。对已验证且存在注册执行器的 Plan 使用该 owner，固定选中账号、分组和模型；不支持的 Plan 明确拒绝。保持在线优先与所有退出路径的槽位释放。worker 路径继续产生现有用量及唯一批样本结算，禁止调用会另行结算的在线 handler。无 schema、任务状态机或 API 字段变更。

范围外：不改变账号平台标签、分组、模型别名、在线路由或报价；不为通用 Key 新增授权组；不扩大输入协议或异地代跑。

验证：正例须覆盖真实类别的冻结 Plan→执行→结果→批量结算；反例覆盖未注册适配器、不兼容 Plan、跨组、排队与重试去重，回归既有 OpenAI 路径。代码验证后独立审批发布。北京同一 Key/模型十条全成功且输出可下载、费用核对后，才执行已授权的单批一万条。历史失败不删除。

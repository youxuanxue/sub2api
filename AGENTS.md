<!-- dev-rules:codex BEGIN — generated, do not edit by hand -->

本节由 `dev-rules/sync.sh` 经 `dev-rules/scripts/gen_codex_agents.py` 确定性生成；请勿手工编辑标记之间的内容（手写说明放到标记之外）。

## 工作宪法（单一事实来源）
- 会话级硬纪律与身份：见 [`dev-rules/global/CLAUDE.md`](dev-rules/global/CLAUDE.md)。Codex 与 Claude Code、Cursor 共用同一份宪法。

## 行为规则（按需展开阅读）
Codex 不自动加载 `.cursor/rules/*.mdc`；需要时按下表路径读取对应文件：

- [`.cursor/rules/dev-rules-convention.mdc`](.cursor/rules/dev-rules-convention.mdc) — 规则、技能与入口的源头、同步和提交纪律；安装/故障细节按需读取。
- [`.cursor/rules/product-dev.mdc`](.cursor/rules/product-dev.mdc) — 研发风险、执行路径、提交与 PR 门禁；默认单 PR 直接实现。
- [`.cursor/rules/test-philosophy.mdc`](.cursor/rules/test-philosophy.mdc) — 按风险匹配测试；核心行为验证常驻，完整 Story 细则按需读取。

## 可用技能（progressive disclosure）
优先使用会话提供的技能目录；需要发现项目技能时读 [技能索引](.cursor/skill-index.md)，再只读匹配的 `SKILL.md`。正文中的参考文档按触发条件读取，不全量展开。

## 全局技能与工具
- 代码审查走三端通用 skill `xj-review`：先跑 `preflight.sh` 取 ground-truth，再按风险分级审；Codex 里描述"review 这个 diff/PR"即触发。

<!-- dev-rules:codex END -->
## 候选资格 SSOT（Candidate Eligibility SSOT）

协作检索名：`candidate-eligibility-ssot`。授权范围内统一调度的策略、实现与验收边界见
[`docs/approved/candidate-eligibility-ssot.md`](docs/approved/candidate-eligibility-ssot.md)。
该文档 §Implementation/Owners 表是**唯一**的 owner 清单：本文件与 `CLAUDE.md` 都只留
指针，不再复制第二份名单（三处副本已经各自漂移过）。新增 candidate 入口同时进那张表和
`scripts/sentinels/gateway-tk.json`。

不变式：模型及 converter 合法性由 `protocolrouter.Plan` 裁决；现有可用性、saturation
与供应源凭据故障 owner 继续共享；执行读取实际账号和 Plan，计费组平台不参与选号或
handler 选择。测试及发布前缺口见 US-050，本地实现不代表已部署。

## Trajectory SSOT

`traj-ssot`：会话导出契约与唯一 owner 清单见
[`docs/approved/qa-bundle-session-export.md`](docs/approved/qa-bundle-session-export.md)。
QA Bundle 是用户会话导出的唯一数据来源；采集/归档/授权沿用 QA lifecycle，
不恢复旧 prod traj export 或平行投影器。

## Public Quickstart 与注册承诺 SSOT

公开与登录态接入指南、注册入口和返回路径的 owner 清单见
[`docs/approved/public-quickstart-registration-offer.md`](docs/approved/public-quickstart-registration-offer.md)。
页面只做编排；配置生成和注册承诺必须复用该文档中的共享 owner。

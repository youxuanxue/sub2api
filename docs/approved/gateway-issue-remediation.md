---
title: 网关 issue 复核后的协议与计量修复
status: approved
approved_by: "feng（本次对话：乔布斯，统筹，安排几个Agent并行，去核实、修复、提交并推送pr。）"
approved_at: 2026-10-30
---

# 网关 issue 复核后的协议与计量修复

## 范围与授权

承接排除 upstream merge 的线上 issue 复核，落实可复现的现有请求链缺陷；授权实现、测试、提交及推送 PR，合并和部署另行确认。本契约记录本次修复边界，不改变模型上架、账号供给、定价或路由授权政策。Web impact: none。

## 行为契约

- Anthropic SSE：合法非负整数 usage 字段以字段存在性覆盖旧值，显式零有效；缺失或非法值不清空已有量。canonical cache read 存在时优先于 alias，写入总量显式零清除旧 TTL 明细。保留供应商 inclusive input 规范化；未知 TTL 差额价格不在本次裁决。
- Kiro：仅识别一次起始 inline thinking envelope；进入正文后标签字面值完整保留。起始未闭合 reasoning 继续隐藏，原生 reasoning 事件保持独立。裸标签起始的字面示例与旧协议无法区分，继续保守隐藏。
- Antigravity：SDK response schema aliases 转为 v1internal responseSchema；canonical 存在则优先（含非法值），响应 schema 不注入工具参数占位字段。共享原有 schema 清理算法，修正字段和根节点用途，不承诺支持任意 JSON Schema。复审确认的递归引用会造成进程栈溢出，须在共享清洗入口有界展开：递归或超出展开预算的 schema 明确返回参数错误，不能截断约束后继续上游调用；正常无环复用保持可用。
- Antigravity Gemini wire：复用既有锚定 system 开头的 Claude SDK identity/attribution 清理，保留项目规则、后续提及及用户正文；已有 Antigravity identity 不能跳过清理。
- 动态工具：在 Responses 可表达的边界按消息顺序传递 additions、工具引用与名称映射；无法无损表达的删除或非法引用在调用上游前明确返回参数错误，不能静默丢弃后继续计费。

- Chat→Responses 缓存：保留 key 与支持的 retention，Codex OAuth 去掉不支持的 explicit mode/断点；其他 API-key 供应先保留，只有上游明确拒绝才使用共享预算重试。重试不改变账号身份隔离，不在响应开始、取消或错误体截断后继续。

## 未解决项

#4632 的真实零缓存原因尚未证实；#8393 菜单下架但保留 mapping 可访问符合预期，独立 quota pool 证据不足。不会通过盲目改缓存参数或拆冷却声称解决。#8426 的 native-v2 密文 state 必须绑定实际产生它的账号，现有可过期、可覆盖的 soft sticky 不能代替该 owner，本次不据其强绑。未知 TTL 差额及支付查单能力另行裁决。

## 验证与回退

复现测试先失败后通过；覆盖非法/缺失/零值、跨块标签、原生与转换请求和工具时序。preflight 与独立交叉审查后提交。无需数据迁移，回退使用 git revert。线上错误关键词为零不能证明静默问题从未触发；本次离线验证不冒称线上部署验收。

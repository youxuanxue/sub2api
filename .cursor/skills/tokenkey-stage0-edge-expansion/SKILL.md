---
name: tokenkey-stage0-edge-expansion
description: >-
  Use when a request asks to add a new TokenKey EC2/CFN Edge and must be distinguished from migrating an existing Lightsail Edge.
---

# 新增 EC2/CFN Edge 请求路由

当前 EC2 workflow 只服务 `edge-targets.json` 中已有的 migration candidate 或 active owner，不是任意
新 Edge 的扩容入口。

- 迁移现有 Lightsail Edge 到 EC2：用 `tokenkey-stage0-edge-platform-migration`。
- 新增全新 Edge：用 `tokenkey-stage0-edge-lightsail-expansion`。
- 不要把全新 Edge 擅自写成 EC2 `migration_candidate`；这会绕过已有迁移数据与 owner 契约。

若业务明确要求“全新 Edge 也直接建在 EC2”，这是新的平台决策，先走高风险设计审批。

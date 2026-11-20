---
title: TokenKey 双市场首页与统一产品矩阵
status: approved
approved_by: feng (对话审批 2026-09-02)
authors: [agent]
created: 2026-09-02
revised_at: 2026-11-20
depends_on:
  - docs/approved/design-apex-domain-phase2.md
  - docs/approved/user-cold-start.md
superseded_partial_by:
  - docs/approved/design-callmodel-product-facade.md
---

# TokenKey 双市场首页与统一产品矩阵

> **海外 host / 跨域踢出 / CTA**：以 [`design-callmodel-product-facade.md`](design-callmodel-product-facade.md) 为准。
> 本文件只保留仍有效的「双门面、单产品」不变量；海外仅首页 + 非首页 30x 等历史合同已删除。

## 0. 一句话目标

TokenKey 仍然只有一个产品、一个账户和一套 API；由 hostname 决定品牌铬与首页模型叙事。

- `callmodel.io`：主商业门面（完整 SPA，见门面文档）
- `tokenkey.dev`：国内/既有门面 + **唯一 Admin**
- `api.callmodel.io` / `api.tokenkey.dev`：同构机器入口

## 1. 产品不变量

| 能力 | 唯一事实来源 |
| --- | --- |
| 用户身份 | 同一 user ID；登录态 Cookie 按当前门面 host（互不共享） |
| 资产 / Key / 模型目录 / 计费 | 同一后端与数据 |
| API | 两 API host 同构 `@machine` allowlist（见 apex / 门面） |
| 发布物 | 同一前端构建；禁止第二套前端工程 |
| Admin | **仅** `tokenkey.dev` |

## 2. Hostname 是唯一市场判定源

<!-- hostname-allowlist-and-redirect: historical redirect rules superseded by design-callmodel-product-facade.md -->

市场/品牌判定只读 hostname，不读 path、query 或 cookie。具体海外 host 行为与 allowlist/redirect
以门面文档为准；本文不再维护第二套 hostname 表。

冷启动与注册承诺见 [`user-cold-start.md`](user-cold-start.md)。

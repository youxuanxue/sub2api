---
title: Trial media 402 UX + users.total_recharged SSOT with balance history panel
status: approved
approved_by: feng
approved_at: 2026-10-09
authors: [agent]
created: 2026-10-09
related_incident: rt@tk.com (user_id=61) 402 trial_unpaid_media_blocked 2026-10-07..2026-10-09
related_prs: [2503]
decisions: [A1, B1]
---

# Trial media 402 UX + total_recharged SSOT

## 0. TL;DR

客户号在管理员加款后仍可能被媒体门禁当成「未充值试用」：根因是 `users.total_recharged` 与后台「总充值」（`SumPositiveBalanceByUser`）口径分叉。本设计修两件事：

1. **Studio UX**：`trial_unpaid_media_blocked` / 402 显示中文说明 +「去充值」入口（对齐已有 `insufficient_balance` 模式）。
2. **充值 SSOT**：统一「总充值」定义；写路径维护 `users.total_recharged`；存量回填；面板与门禁读同一口径。

**不做**：改门禁去热路径查 `redeem_codes`；关闭全局 `trial_unpaid_media_blocked`；把注册赠额算成已充值。

## 1. 背景（已核实）

- 门禁：`EvaluateTrialUnpaidMedia` — `TotalRecharged<=0` 且 `Balance<=max`（默认 $2）则 402。
- 面板总充值：`redeem_codes` 上 `type IN (balance, admin_balance)` 且 `value>0` 之和（**含**注册赠额 journal）。
- 管理员 `AdjustBalance` / `SetBalance` **不**写 `users.total_recharged`；只写 `redeem_codes` journal。
- `rt@tk.com`：开户 admin $100 + 2026-10-09 admin +$1000；`users.total_recharged=0`；烧到 ≤$2 后连续 402；加款后靠 `Balance>$2` 旁路恢复。客户号，不是内部号。

若把 `users.total_recharged` **原样**对齐未过滤的面板合计，则凡有注册赠额（`admin_balance` + `BalanceGrantNoteSignup`）的用户 `TotalRecharged>0`，媒体门禁失效。

## 2. 总充值口径（需人工确认）

**定义（推荐）**：计入「已完成充值 / 总充值」的正额入账：

| 来源 | 计入？ | 理由 |
|---|---|---|
| `type=balance`（支付履约 / 用户兑换码） | **是** | 真实充值 |
| `type=admin_balance`，notes 为空或运营自定义备注 | **是** | 对客户的人工充值（如 rt 的 +$1000） |
| `type=admin_balance`，notes=`开户期初余额（管理员）` | **是** | 客户开户注资，非注册机赠额 |
| `type=admin_balance`，notes=`注册初始余额` | **否** | 冷启动赠额，门禁必须继续挡住 |
| `type=admin_balance`，notes=`邀请试用赠予` | **否** | 同上 |
| `type=admin_balance`，notes=`OAuth首次绑定默认余额` | **否** | 同上 |
| 负向 `admin_balance`（扣减） | **不计入正合计** | 与现面板 `value>0` 一致；不从 `total_recharged` 回滚（累计充值只增不减，与今日 `AddTotalRecharged` 习惯一致） |

常量 owner：`BalanceGrantNoteSignup` / `InviteTrial` / `OAuthFirstBind` 为**排除集**；`BalanceGrantNoteAdminOpening` **计入**。

### 决策点 A（已批）

- [x] **A1** 采用上表：排除三类自动赠额 notes，其余正额 `balance`/`admin_balance` 计入。
- [ ] ~~A2~~ 否决。

### 决策点 B（已批）

- [x] **B1** 二者都改用上表过滤后的合计（面板不再把注册赠额算进「总充值」；赠额仍出现在变动明细里）。
- [ ] ~~B2~~ 否决。

## 3. UX（Studio）

| 项 | 行为 |
|---|---|
| 识别 | `error.code === trial_unpaid_media_blocked` 或 message 含 completed recharge / trial_unpaid_media |
| 文案（zh） | 图片/视频生成需完成充值后可用；文本模型仍可使用试用余额。 |
| 文案（en） | 保持与 API 英文义一致 |
| CTA | 与 `insufficient_balance` 相同：`router-link` → `/purchase`「去充值」 |
| 表面 | ImageStudio / VideoStudio / BakeOff |
| API 响应 | **不改**英文 `message` 与 `code`（避免破坏 API 客户端）；仅 Web Studio 本地化 |

## 4. 写路径与回填

**Forward**

1. 抽出共享 helper：`IsGiftBalanceGrantNote(notes) bool` + `SumQualifyingRecharged(...)`（与面板/回填同一过滤）。
2. 管理员余额 add/set 正差额：在 `updateUserBalanceWithLedgerTx` 成功后对正 `balanceDiff` 执行 `AddTotalRecharged`（或与 Adjust 同事务更新）。
3. `writeBalanceGrantLedger` / 注册开户：仅当 notes **不在**排除集时同步 `AddTotalRecharged`；Signup/Invite/OAuth 赠额只动 `balance` + journal，不碰 `total_recharged`。
4. 已有 `UpdateBalance`（支付/兑换）保持 `AddTotalRecharged`；promo 默认走 `UpdateBalance` 则**计入字段**（若需排除另开决策）。**已知例外（本 PR 不改）**：promo 等只调 `UpdateBalance`、不写 `redeem_codes` journal 的路径会使 `users.total_recharged` **≥** 面板 `SumPositiveBalanceByUser`；门禁读字段（偏放行），面板读 journal（偏保守）。journal 对齐另开决策，不在本变更范围。

**Backfill（一次性，可重复执行）**

```text
UPDATE users u
SET total_recharged = COALESCE((
  SELECT SUM(r.value) FROM redeem_codes r
  WHERE r.used_by = u.id AND r.value > 0
    AND r.type IN ('balance', 'admin_balance')
    AND COALESCE(r.notes, '') NOT IN (
      '注册初始余额', '邀请试用赠予', 'OAuth首次绑定默认余额'
    )
), 0)
WHERE u.deleted_at IS NULL;
```

回填后对活跃 API key 触发 auth cache 失效（或短 TTL 自然过期）。`rt@tk.com` 期望：`total_recharged >= 1000`（若 A1 含开户则为 1100）。

## 5. 验收

| # | 场景 | 期望 |
|---|---|---|
| P1 | Studio 触发 402 trial media | 中文说明 + 去充值链到 `/purchase` |
| P2 | 新用户仅注册赠额，balance≤$2，生图 | 仍 402 |
| P3 | 管理员对客户 add 正余额后 | `users.total_recharged` 增加同等差额；面板总充值一致 |
| P4 | 支付/兑换码正入账 | 与今日一致，计入 |
| N1 | 英文 API 客户端 | `code`/`message` 不变 |
| R1 | 回填后 rt@tk.com | `total_recharged` 与过滤后 redeem 合计一致；余额≤$2 时若已计入充值则生图放行 |

## 6. 风险

- **高**：计费字段语义 + 媒体安全门禁放行条件。
- **常规**：Studio i18n/CTA。
- 回填会让「曾获 admin 正加款但已烧光余额」的客户重新获得生图资格——对客户号是预期修复，不是旁路。

## 7. 非目标

- 不改 `trial_unpaid_media_max_balance` 默认值。
- 不在本 PR 做支付产品文案大改。
- 不做全站错误中文化中间件。

## 8. Implementation notes (2026-10-09)

- Forward: `writeBalanceGrantLedger` / `bestEffortBalanceGrantLedger` + `AddTotalRecharged`.
- Panel: `SumPositiveBalanceByUser` excludes `GiftBalanceGrantNotes()`.
- Backfill: `backend/migrations/tk_102_backfill_total_recharged_qualifying.sql`.
- Studio: `trial_unpaid_media` error code + `/purchase` CTA; API English message unchanged.
- Auth cache: L1/L2 TTL will refresh stale `total_recharged` snapshots after deploy; balance>$2 already bypasses the gate.
- Promo / other `UpdateBalance`-only credits: field may exceed panel sum until those paths journal; accepted pre-existing exception (see §4 Forward #4).

## Implementation owners

| 行为 | 唯一 owner / 调用方 |
|---|---|
| 赠额排除集与事务内累计充值 | `backend/internal/service/balance_grant_ledger_tk.go`；notes 原文精确匹配，与面板和回填一致 |
| 管理员余额变动 | `admin_user_tk_balance.go` 调用事务 writer；`admin_user.go` 开户返回对象反映已提交累计充值 |
| Studio 错误分类与充值资格 | `frontend/src/utils/studioGatewayError.tk.ts` |
| Studio 错误与充值入口展示 | `frontend/src/views/user/studio/components/StudioGatewayError.vue`；ImageStudio / VideoStudio / BakeOff 只传入消息和错误码 |

验收映射：`.testing/user-stories/stories/US-061-trial-media-recharge.md`。
`gateway-tk` / `frontend-tk` sentinel 同时锚定关键调用、实现与回归测试。

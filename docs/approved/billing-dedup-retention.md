---
title: 计费去重表保留期：热表 7 天、含归档共 30 天
status: approved
approved_by: "product owner (2026-11-09 conversation: 保留 7 天就绰绰有余了。你可以看下上游仓库的建议。; 同意建议。请处理之后，我们再做数据机的拆分。)"
approved_at: 2026-11-09
created: 2026-11-09
authors: [claude]
risk: high
---

# 计费去重表保留期

## 决策

- `usage_billing_dedup` 热表保留 **7 天**，热表加归档表合计保留 **30 天**。超过 30 天的去重键直接删除，不进归档表。
- 保留期写死在 `backend/internal/service/data_lifecycle_policy_tk.go`，不读配置。配置里的 `usage_billing_dedup_days` 以前就不生效，现在也不生效。
- 上游文件只改一行：`ops_cleanup_service.go` 改为调用 TK 的 `cleanupUsageBillingDedup`。归档表清理和过期键删除放在 TK 文件里，通过可选接口 `UsageBillingDedupArchiveCleaner` 接入，上游接口和测试桩不变。

## 这张表做什么

只做一件事：防止同一个资金事件被扣两次。计费前插入 `(request_id, api_key_id)`，冲突就不再扣费。

| 读写方 | 位置 |
|---|---|
| 正常计费 | `usage_billing_repo.go` `Apply` |
| 批量图片冻结、扣款、释放 | `usage_billing_repo.go`；释放前用 `batchImageHoldClaimExists` 确认冻结键还在热表或归档表 |
| 视频退款 | `usage_billing_repo_tk_video_refund.go` |
| 超期迁入归档表 | `dashboard_aggregation_repo.go` `CleanupUsageBillingDedup`（上游） |

用户用量明细在 `usage_logs`，账单在 `billing_usage_entries`，仪表盘在 `usage_dashboard_*`，余额在 `users.balance`。这张表只有 `request_id`、`api_key_id`、请求指纹和时间，没有金额和 token，不参与任何统计。

## 为什么 7 天够用

上游默认热表 365 天，归档表永不清理。原因是上游优先用客户端传来的 ID 做键（`client:` 前缀），客户端隔很久用同一个 ID 重发也要拦下。

TK 在 #2113（2026-09-11）改成不信任客户端 ID，只用网关为每个请求新生成的 UUID（`local:` 前缀）。键不会被再次使用，去重只需要覆盖同一个资金事件内部的重复写入。

2026-11-09 prod 只读实测：

| 测量 | 结果 |
|---|---|
| 近 30 天 `usage_logs` 同一键出现多次 | 6 个键，各多 1 行，间隔 0.0 秒 |
| 去重键写入到用量记录落地（近 3 天 339,940 行） | 中位数、99 分位、最长都是 0 秒 |
| 近 7 天去重键没有对应用量记录 | 0 |
| `grok-video:` 键计费到用量记录 | 0 秒（21 个键） |

## 为什么归档要 30 天

几类异步资金事件从抢占键到结算的最长间隔：

| 资金事件 | 最长间隔 | 来源 |
|---|---|---|
| 批量图片冻结后释放 | 7 天 | `batch_image.inflight_ttl_seconds` 默认 604800 |
| Grok 视频计费 | 48 小时 | `grokVideoBilledClaimTTL` |
| 托管批量任务 | 24 小时截止 | `managed_batch_backend.go` |
| 视频退款 | 6 次重试内 | `videoRefundMaxAttempts` |

批量图片释放冻结金额前要确认冻结键还在。键已被删时释放会被跳过，用户的冻结余额回不来。所以热表加归档表必须活得比 7 天长，30 天是 7 天的四倍多。`TestBillingDedupArchiveOutlivesSlowestSettlement` 守住这条：归档窗口至少是最慢结算的两倍。

## 清理顺序

每晚 `ops.cleanup` 一次（默认 02:00）：

1. **热表删除超过 30 天的键**，不归档。每次最多 100 万行。
2. **上游的热表迁归档**（超过 7 天的键）。第 1 步还没删完时跳过这一步，否则积压的过期键会先被复制进归档表、再被删掉。
3. **归档表删除超过 30 天的键**，每次最多 100 万行。

## 上线后的积压

2026-11-09 只读实测：

| | 超过 30 天 | 7–30 天 | 7 天内 | 合计 | 归档表 |
|---|---:|---:|---:|---:|---:|
| prod | 14,456,771 | 2,999,003 | 759,975 | 18,215,749 | 0 |
| cn-prod | 0 | 459,477 | 522,820 | 982,297 | 0 |

prod 超过 30 天的 1,446 万行按每晚 100 万行，第 1 步约 15 个晚上删完，期间第 2 步跳过，归档表保持为空。之后进入稳态：热表约 7 天（prod 约 76 万行），归档表约 23 天（prod 约 300 万行）。cn-prod 没有积压，第一晚就进入稳态。

删除不会让表文件变小，物理恢复也会保留已有空页。热表超过 7 天的归档积压收敛后，用 `REINDEX INDEX CONCURRENTLY` 重建膨胀索引；不做 `VACUUM FULL`。执行期间保留正常计费与归档，先确认没有长期事务、无效索引和备份/维护冲突，见 [`design-data-plane-split.md`](design-data-plane-split.md)。

## 受影响的运维工具

- **COS 恢复验收** `ops/backup/cos_restore_acceptance.py`：以前比较整表 `usage_logs` 与 `usage_billing_dedup` 的行数差。每晚清理会让整表行数变化，所以改为只在固定的 3 天窗口内比较这对表。窗口起点由线上时钟定一次，终点为该备份开始前已完成的整小时；相同的上下界同时用于线上快照和恢复副本，排除备份边界正在异步落账的请求。早于窗口的恢复点在窗口内没有行，只做其余检查。去重表也不再参与「恢复点不得超过线上」的判定。
- **冻结增量** `ops/migration/standard_freeze_delta.py`：`append_only` 策略假设这张表只追加。跨过一次每晚清理时会按设计 fail closed，改用 `refresh` 策略或冻结前暂停清理。已写进 `ops/migration/README.md`。

## 验证

- 单元测试：保留期、清理顺序、积压时跳过迁归档、报错时停止、旧接口回退、窗口比较。
- 集成测试（真实 Postgres）：`TestUsageBillingDedupLifecycle_PurgesExpiredAndKeepsWindow`，覆盖热表删除不归档、窗口内迁归档、归档过期删除。
- 已有的 `TestUsageBillingRepositoryApply_DeduplicatesAgainstArchivedKey` 仍通过：归档表里的键照样能拦住重复扣费。

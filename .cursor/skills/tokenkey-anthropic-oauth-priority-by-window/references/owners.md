## 9. 扩展阅读

- `ops/anthropic/rebalance-anthropic-priority.py`（4 阶段 orchestrator，本 skill 唯一推荐入口）
- `deploy/aws/stage0/anthropic-oauth-priority-rebalance-apply-template.sql`（apply 模板，单账号单字段 update + DO-block 校验）
- `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`（`tiers.lN.baseline.account.priority` 是 tier_base 的源头）
- `backend/internal/service/ratelimit_service.go` — `UpdateSessionWindow`、`calculateAnthropic429ResetTime`（utilization 被动采样的实际写入路径）
- `backend/ent/schema/account.go` — `priority`、`session_window_end` 字段声明 + `(platform, priority)` 复合索引
- [`tokenkey-anthropic-oauth-config`](../../tokenkey-anthropic-oauth-config/SKILL.md) — tier baseline 写入流水线（本 skill 的上游协作方）

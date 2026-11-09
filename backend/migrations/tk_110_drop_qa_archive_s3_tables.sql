-- TK: QA S3 已退役（2026-11-08 用户决定；现行路径为本地 24h + node1 每 4h）。
-- 删除只服务于 S3 上传/校验/缺口审批的三张表：
--   qa_archive_segment_records   (tk_070) segment 成员明细，prod 约 8.1M 行 / 1.77 GB
--   qa_archive_segments          (tk_070) segment 上传/校验状态
--   qa_archive_gap_decision_receipts (tk_075) 缺口审批回执，含 append-only 触发器
-- 保留 qa_archive_shards：本地 24h 清理仍以 generation=0 行记录
-- source_dropped_at / hot_files_cleaned_at，作为可续跑的 DROP 回执。
--
-- bluegreen-safe-destructive-ok: #3108 已删除 S3 writer/reconciler/gap_decision 与
-- qa-maintenance 网关子命令，main 上网关、qa-local-maintenance 与 archive 控制面都
-- 不再读写这三张表。旧 color（≤1.8.353）的服务路径从不触碰它们；其中的
-- qa-maintenance/qa-boundary 子命令只由已 mask 的主机 timer 调用。
-- 前置条件（2026-11-09 已满足）：两台主机的 qa-local-maintenance 已换成 #3108 之后
-- 的构建（sha256 418d8249…），旧二进制的 InspectCatchupHourTx 会 JOIN segment 表。
--
-- 锁与耗时：DROP TABLE 只改 catalog 并在提交后 unlink 数据文件，不扫描行；
-- 期间对三张表各持 ACCESS EXCLUSIVE 锁，持锁时间是毫秒级到秒级（取决于文件系统
-- unlink 1.77 GB 的速度），与行数无关。qa_archive_segments 对 qa_archive_shards
-- 有外键，DROP 时还会对 qa_archive_shards 短暂持 ACCESS EXCLUSIVE 锁以移除外键
-- 触发器；该表只有每 5 分钟一次的 qa-local-maintenance cleanup 访问（lock_timeout 100ms，失败下轮重试）。
-- 顺序：先子表（segment_records 引用 segments），再父表；不用 CASCADE，
-- 以免意外带走未知依赖对象。

DROP TABLE IF EXISTS qa_archive_segment_records;
DROP TABLE IF EXISTS qa_archive_segments;

DROP TABLE IF EXISTS qa_archive_gap_decision_receipts;
DROP FUNCTION IF EXISTS tk_qa_archive_gap_decision_receipts_immutable();

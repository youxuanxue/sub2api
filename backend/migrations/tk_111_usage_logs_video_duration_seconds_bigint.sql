-- TK: usage_logs.video_duration_seconds 统一为 BIGINT。
--
-- 这一列由两条迁移各自 ADD COLUMN IF NOT EXISTS：tk_025（TokenKey，2026-06）写的是
-- BIGINT，上游 172_video_per_second_billing_metadata 写的是 INTEGER。谁先执行谁定类型：
-- prod 先跑 tk_025，是 bigint；cn-prod 在 2026-10-14 新建，172 先跑，成了 integer。
-- ent schema 定义的是 Int64，所以以 BIGINT 为准。
--
-- cn-prod 已在 2026-11-11 23:06Z 低峰手工改成 bigint（39 秒，锁表期间无写入）；
-- prod 本来就是 bigint。所以这条迁移在两个线上库都只做一次类型检查，不改写数据。
-- 它只在新建的库上真正执行：那时表是空的，ALTER TYPE 瞬间完成。
--
-- bluegreen-safe-destructive-ok: integer -> bigint 是放宽，旧版本写入的整数值都能
-- 存进 bigint，旧 color 的读写不受影响。只在列仍为 integer 时执行；那会重写表并持有
-- ACCESS EXCLUSIVE 锁，lock_timeout 保证拿不到锁就直接失败，不会排在写入前面把它们堵住。
DO $$
BEGIN
    IF (
        SELECT format_type(atttypid, atttypmod)
        FROM pg_attribute
        WHERE attrelid = 'usage_logs'::regclass
          AND attname = 'video_duration_seconds'
          AND NOT attisdropped
    ) = 'integer' THEN
        SET LOCAL lock_timeout = '5s';
        ALTER TABLE usage_logs ALTER COLUMN video_duration_seconds TYPE BIGINT;
    END IF;
END $$;

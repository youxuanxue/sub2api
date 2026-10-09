-- high-risk-anchor: docs/approved/trial-media-recharge-ux-and-total-recharged.md
-- A1/B1: align users.total_recharged with qualifying 总充值 (panel filter).
--
-- Qualifying = redeem_codes value>0, type IN (balance, admin_balance),
-- excluding automatic gift notes (signup / invite trial / OAuth first-bind).
-- Admin opening balance and operator top-ups are included.
--
-- Idempotent: re-running recomputes the same sum.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

UPDATE users AS u
SET total_recharged = COALESCE((
    SELECT SUM(r.value)
      FROM redeem_codes AS r
     WHERE r.used_by = u.id
       AND r.value > 0
       AND r.type IN ('balance', 'admin_balance')
       AND COALESCE(r.notes, '') NOT IN (
           '注册初始余额',
           '邀请试用赠予',
           'OAuth首次绑定默认余额'
       )
), 0)
WHERE u.deleted_at IS NULL;

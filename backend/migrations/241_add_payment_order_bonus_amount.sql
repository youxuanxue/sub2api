-- 充值赠送额度：余额充值订单命中赠送阶梯时的赠送 USD 金额。
-- 已计入 payment_orders.amount（到账总额），单独落列用于订单展示与推广返利基数剔除。
-- ADD COLUMN IF NOT EXISTS + DEFAULT 0 is online-safe: new column is nullable-in-practice
-- for existing rows via the default, no rewrite of amount, rollback is DROP COLUMN.
-- bluegreen-safe-destructive-ok
ALTER TABLE payment_orders ADD COLUMN IF NOT EXISTS bonus_amount DECIMAL(20,2) NOT NULL DEFAULT 0;

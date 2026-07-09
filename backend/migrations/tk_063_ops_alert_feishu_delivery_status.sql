-- tk_063_ops_alert_feishu_delivery_status.sql
--
-- Persist Feishu delivery evidence per alert event. A single alert event can
-- produce two independent cards: the firing card and the paired recovery card.
-- Keeping separate columns avoids the common post-mortem ambiguity where a
-- resolved event overwrites the firing-card evidence.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE ops_alert_events
  ADD COLUMN IF NOT EXISTS feishu_firing_sent BOOLEAN DEFAULT false,
  ADD COLUMN IF NOT EXISTS feishu_firing_sent_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS feishu_firing_status TEXT DEFAULT '',
  ADD COLUMN IF NOT EXISTS feishu_firing_error TEXT DEFAULT '',
  ADD COLUMN IF NOT EXISTS feishu_recovery_sent BOOLEAN DEFAULT false,
  ADD COLUMN IF NOT EXISTS feishu_recovery_sent_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS feishu_recovery_status TEXT DEFAULT '',
  ADD COLUMN IF NOT EXISTS feishu_recovery_error TEXT DEFAULT '';

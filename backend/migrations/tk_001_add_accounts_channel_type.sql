-- Add channel_type: selects New API adaptor on fifth platform `newapi` (required); optional on legacy four-platform accounts.
ALTER TABLE accounts
  ADD COLUMN IF NOT EXISTS channel_type INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_accounts_channel_type
  ON accounts(channel_type);

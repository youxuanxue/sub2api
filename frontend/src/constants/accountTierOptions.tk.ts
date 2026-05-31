/**
 * TokenKey-only: account stability tier options for the "设置 Tier" admin action.
 *
 * The tier ids must stay in sync with the embedded backend baseline
 * (backend/internal/baseline/anthropic-oauth-stability-baselines-tiered.json,
 * tier_order l1..l5). The backend is the single source of truth for the tier
 * VALUES (concurrency / rpm / sessions / TLS); this file only carries display
 * labels for the dropdown, so it deliberately holds no numeric config.
 */

export interface AccountTierOption {
  value: string
  label: string
  /** short human hint shown under the option */
  hint: string
}

export const ACCOUNT_TIER_OPTIONS: AccountTierOption[] = [
  { value: 'l1', label: 'L1', hint: 'concurrency 2 · base_rpm 7 · max_sessions 30' },
  { value: 'l2', label: 'L2', hint: 'concurrency 4 · base_rpm 14 · max_sessions 60' },
  { value: 'l3', label: 'L3', hint: 'concurrency 6 · base_rpm 21 · max_sessions 90' },
  { value: 'l4', label: 'L4', hint: 'concurrency 8 · base_rpm 28 · max_sessions 120' },
  { value: 'l5', label: 'L5', hint: 'concurrency 10 · base_rpm 28 · max_sessions 150' },
]

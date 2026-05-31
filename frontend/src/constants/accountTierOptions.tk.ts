/**
 * TokenKey-only: fallback account stability tier options for the "设置 Tier"
 * admin action.
 *
 * The backend tiers table (projection of the git baseline,
 * backend/internal/baseline/anthropic-oauth-stability-baselines-tiered.json,
 * tier_order l1..l5) is the SINGLE SOURCE OF TRUTH for per-tier VALUES. The
 * picker (useTkAccountTier) fetches them live from GET /admin/tiers and renders
 * the numeric hints from that response. This constant is ONLY a label fallback
 * if that fetch fails — so it deliberately carries no numeric config (hardcoding
 * numbers here drifts the moment the baseline is bumped).
 */

export interface AccountTierOption {
  value: string
  label: string
  /** short human hint shown under the option (populated live from the tiers table) */
  hint: string
}

export const ACCOUNT_TIER_OPTIONS: AccountTierOption[] = [
  { value: 'l1', label: 'L1', hint: '' },
  { value: 'l2', label: 'L2', hint: '' },
  { value: 'l3', label: 'L3', hint: '' },
  { value: 'l4', label: 'L4', hint: '' },
  { value: 'l5', label: 'L5', hint: '' },
]

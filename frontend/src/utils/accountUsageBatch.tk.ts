/**
 * Shared passive-usage batch eligibility (#1031 / useTkAccountUsageBatch).
 *
 * Single source of truth for which admin list rows are served by
 * POST /admin/accounts/usage/batch (mirrors backend GetPassiveUsageBatch gates).
 */
import type { Account } from '@/types'

/** Accounts whose upstream adapter reports TokenKey-local 5h/7d billing windows. */
export function usesLocalUsageWindows(account: Account): boolean {
  if (account.platform === 'newapi' || account.platform === 'grok') return true
  return account.platform === 'antigravity' && account.type !== 'oauth'
}

/** Rows whose list load is satisfied by the batch passive endpoint (no mount fan-out). */
export function isBatchPassiveCapable(account: Account): boolean {
  if (account.platform === 'kiro') return true
  if (account.platform === 'openai' && account.type === 'oauth') return true
  if (usesLocalUsageWindows(account)) return true
  return (
    account.platform === 'anthropic' &&
    (account.type === 'oauth' || account.type === 'setup-token')
  )
}

/** Passive source on mount when the cell self-fetches (non-batch / no override path). */
export function usesPassiveUsageOnMount(account: Account): boolean {
  return isBatchPassiveCapable(account)
}

/** Whether AccountUsageCell may ever load usage for this account. */
export function canSelfFetchUsage(account: Account): boolean {
  if (isBatchPassiveCapable(account)) return true
  if (account.platform === 'gemini') return true
  return account.platform === 'antigravity' && account.type === 'oauth'
}

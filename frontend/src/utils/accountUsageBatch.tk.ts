/**
 * Shared passive-usage batch eligibility (#1031 / useTkAccountUsageBatch).
 *
 * Single source of truth for which admin list rows are served by
 * POST /admin/accounts/usage/batch (mirrors backend GetPassiveUsageBatch gates).
 */
import type { Account } from '@/types'
import { isOfficialGeminiBaseUrl } from './geminiExplicitCache'
import { PLATFORM_ANTHROPIC, PLATFORM_ANTIGRAVITY, PLATFORM_GEMINI, PLATFORM_GROK, PLATFORM_KIRO, PLATFORM_NEWAPI, PLATFORM_OPENAI } from '@/constants/gatewayPlatforms'

/** Accounts whose upstream adapter reports TokenKey-local 5h/7d billing windows. */
export function usesLocalUsageWindows(account: Account): boolean {
  if (account.platform === PLATFORM_NEWAPI || account.platform === PLATFORM_GROK) return true
  return account.platform === PLATFORM_ANTIGRAVITY && account.type !== 'oauth'
}

/** Rows whose list load is satisfied by the batch passive endpoint (no mount fan-out). */
export function isBatchPassiveCapable(account: Account): boolean {
  if (account.platform === PLATFORM_KIRO) return true
  if (account.platform === PLATFORM_OPENAI && account.type === 'oauth') return true
  if (usesLocalUsageWindows(account)) return true
  return (
    account.platform === PLATFORM_ANTHROPIC &&
    (account.type === 'oauth' || account.type === 'setup-token')
  )
}

/** Passive source on mount when the cell self-fetches (non-batch / no override path). */
export function usesPassiveUsageOnMount(account: Account): boolean {
  return isBatchPassiveCapable(account)
}

/** Simulated Google quotas apply only to official Gemini OAuth/API accounts. */
export function usesGeminiSimulatedQuota(account: Account): boolean {
  if (account.platform !== PLATFORM_GEMINI || account.type === 'service_account') return false
  return account.type !== 'apikey' || isOfficialGeminiBaseUrl(account.credentials?.base_url as string | undefined)
}

/** Whether AccountUsageCell may ever load usage for this account. */
export function canSelfFetchUsage(account: Account): boolean {
  if (isBatchPassiveCapable(account)) return true
  if (account.platform === PLATFORM_GEMINI) return usesGeminiSimulatedQuota(account)
  return account.platform === PLATFORM_ANTIGRAVITY && account.type === 'oauth'
}

/**
 * TokenKey-only: batch passive-usage loading for the admin accounts list.
 *
 * Keeps AccountsView.vue thin (CLAUDE.md §5). Symptom this fixes: each
 * AccountUsageCell self-fetched GET /admin/accounts/:id/usage on mount, so a
 * page of N Anthropic OAuth/SetupToken rows fired N parallel XHRs (the client
 * usageLoadQueue is a no-op pass-through). This composable fetches all visible
 * Anthropic OAuth/SetupToken rows' passive usage in ONE POST
 * /admin/accounts/usage/batch call, and exposes a per-row override the cell
 * renders verbatim (usageOverride !== undefined => the cell never self-fetches).
 *
 * The server endpoint remains passive-only. Rows it can serve receive that
 * passive payload; rows that would otherwise self-fetch active usage receive a
 * null override on table load, so mounting the account list no longer fans out
 * to upstream usage probes. Explicit user refresh still flows through the
 * cell's manual-refresh token.
 */
import { ref } from 'vue'
import { adminAPI } from '@/api'
import type { Account, AccountUsageInfo } from '@/types'

// Mirrors AccountUsageCell's source='passive' mount condition: only Anthropic
// OAuth/SetupToken rows are served by the batch passive endpoint.
function isBatchPassiveCapable(account: Account): boolean {
  return (
    account.platform === 'anthropic' &&
    (account.type === 'oauth' || account.type === 'setup-token')
  )
}

function canSelfFetchUsage(account: Account): boolean {
  if (isBatchPassiveCapable(account)) return true
  if (account.platform === 'gemini') return true
  if (account.platform === 'antigravity') return account.type === 'oauth'
  if (account.platform === 'openai') return account.type === 'oauth'
  return false
}

export function useTkAccountUsageBatch() {
  // accountID(string) -> usage. Present-with-null is a deliberate signal to the
  // cell: "the list owns your usage, do not self-fetch" (override !== undefined).
  const usageByAccountId = ref<Record<string, AccountUsageInfo | null>>({})
  const usageReqSeq = ref(0)

  /**
   * usageOverrideFor returns the prop value for a given row:
   *  - AccountUsageInfo | null  for rows whose list view owns usage loading;
   *  - undefined                for rows that never self-fetch usage.
   */
  function usageOverrideFor(account: Account): AccountUsageInfo | null | undefined {
    if (!canSelfFetchUsage(account)) return undefined
    const key = String(account.id)
    // Until the batch resolves, return null (still suppresses the per-row XHR;
    // batch-capable cells show "-" briefly, then the override watcher fills it in.
    // Active-only platforms stay null until an explicit refresh.
    return key in usageByAccountId.value ? usageByAccountId.value[key] : null
  }

  /**
   * refreshUsageBatch loads passive usage for all batch-capable accounts in one
   * request. Race-guarded by a sequence number so a stale response can't clobber
   * a newer list. Failure-open: on error the map is left as-is (cells show "-").
   */
  async function refreshUsageBatch(accounts: Account[]): Promise<void> {
    const ids = accounts.filter(isBatchPassiveCapable).map(a => a.id)
    const reqSeq = ++usageReqSeq.value

    if (ids.length === 0) {
      usageByAccountId.value = {}
      return
    }

    try {
      const result = await adminAPI.accounts.getBatchPassiveUsage(ids)
      if (reqSeq !== usageReqSeq.value) return
      const server = result.usage ?? {}
      const next: Record<string, AccountUsageInfo | null> = {}
      for (const account of accounts) {
        if (!canSelfFetchUsage(account)) continue
        const key = String(account.id)
        if (!isBatchPassiveCapable(account)) {
          next[key] = null
          continue
        }
        // Omitted by the server (cannot serve passive) => null => cell shows "-".
        next[key] = server[key] ?? null
      }
      usageByAccountId.value = next
    } catch (error) {
      if (reqSeq !== usageReqSeq.value) return
      console.error('Failed to load batch account usage:', error)
    }
  }

  return { usageByAccountId, usageOverrideFor, refreshUsageBatch }
}

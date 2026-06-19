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
 * Scope is deliberately Anthropic OAuth/SetupToken only — exactly the rows the
 * cell loads with source='passive' on mount, so the override is a byte-identical
 * passive→passive replacement. gemini/antigravity/openai rows keep their own
 * fetch (gemini computes from local logs; openai/antigravity use the active
 * path on mount), so no behavior changes for them. The residual per-cell
 * fetches for those minority platforms are bounded by the p-limit in
 * utils/usageLoadQueue.
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

export function useTkAccountUsageBatch() {
  // accountID(string) -> usage. Present-with-null is a deliberate signal to the
  // cell: "the list owns your usage, do not self-fetch" (override !== undefined).
  const usageByAccountId = ref<Record<string, AccountUsageInfo | null>>({})
  const usageReqSeq = ref(0)

  /**
   * usageOverrideFor returns the prop value for a given row:
   *  - AccountUsageInfo | null  for batch-capable rows (suppresses self-fetch);
   *  - undefined                for all other rows (cell self-fetches as before).
   */
  function usageOverrideFor(account: Account): AccountUsageInfo | null | undefined {
    if (!isBatchPassiveCapable(account)) return undefined
    const key = String(account.id)
    // Until the batch resolves, return null (still suppresses the per-row XHR;
    // the cell shows "-" briefly, then the override watcher fills it in).
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
      for (const id of ids) {
        const key = String(id)
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

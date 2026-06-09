import { describe, it, expect } from 'vitest'
import { isTempUnschedActive } from '@/utils/edgeAccounts.tk'
import type { EdgeAccountSummary } from '@/api/admin/edgeAccounts'

function acct(over: Partial<EdgeAccountSummary>): EdgeAccountSummary {
  return {
    id: 1,
    name: 'tokenkey-edge-us-or1-ls-a',
    platform: 'anthropic',
    type: 'oauth',
    status: 'active',
    schedulable: true,
    is_schedulable: true,
    concurrency: 0,
    priority: 0,
    rate_multiplier: 1,
    created_at: '2026-06-09T00:00:00Z',
    ...over
  }
}

describe('isTempUnschedActive', () => {
  it('returns false when there is no cooldown timestamp', () => {
    expect(isTempUnschedActive(acct({ temp_unschedulable_until: undefined }))).toBe(false)
  })

  it('returns false for an expired cooldown whose reason still persists', () => {
    // The reason is NOT cleared on expiry — this is the exact stale-breadcrumb
    // case that made every once-cooled account read as a live problem.
    const past = new Date(Date.now() - 60 * 60 * 1000).toISOString()
    expect(
      isTempUnschedActive(
        acct({
          temp_unschedulable_until: past,
          temp_unschedulable_reason:
            '{"until_unix":1780992011,"status_code":429,"matched_keyword":"anthropic_upstream_error"}'
        })
      )
    ).toBe(false)
  })

  it('returns true while the cooldown is still in the future', () => {
    const future = new Date(Date.now() + 60 * 1000).toISOString()
    expect(isTempUnschedActive(acct({ temp_unschedulable_until: future }))).toBe(true)
  })
})

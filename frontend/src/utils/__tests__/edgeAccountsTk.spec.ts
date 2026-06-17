import { describe, it, expect } from 'vitest'
import { isTempUnschedActive, toUsageInfo } from '@/utils/edgeAccounts.tk'
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

describe('toUsageInfo', () => {
  it('returns null when the edge reported no usage windows', () => {
    expect(toUsageInfo(acct({ usage: undefined }))).toBeNull()
  })

  it('forwards the 7d Sonnet sub-window so the Edge overview renders the "7d S" bar', () => {
    // Regression: the edge overview previously hard-coded seven_day_sonnet to null,
    // so prod 总台 could not show Sonnet's 7-day window even though the edge had it.
    const info = toUsageInfo(
      acct({
        usage: {
          source: 'passive',
          five_hour: { utilization: 52 },
          seven_day: { utilization: 53 },
          seven_day_sonnet: { utilization: 33, resets_at: '2026-06-22T14:00:00Z' }
        }
      })
    )
    expect(info).not.toBeNull()
    expect(info?.seven_day_sonnet?.utilization).toBe(33)
    expect(info?.seven_day_sonnet?.resets_at).toBe('2026-06-22T14:00:00Z')
  })
})

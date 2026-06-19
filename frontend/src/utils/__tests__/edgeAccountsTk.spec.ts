import { describe, it, expect } from 'vitest'
import {
  isTempUnschedActive,
  toUsageInfo,
  matchesStatusFilter,
  matchesGroupFilter,
  collectGroupNames,
  accountVm
} from '@/utils/edgeAccounts.tk'
import type { EdgeAccountSummary, EdgeAccountsResult } from '@/api/admin/edgeAccounts'

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

describe('matchesStatusFilter', () => {
  const future = new Date(Date.now() + 60 * 1000).toISOString()
  const past = new Date(Date.now() - 60 * 1000).toISOString()

  it('matches everything when the filter is empty (all)', () => {
    expect(matchesStatusFilter(acct({ status: 'error' }), '')).toBe(true)
  })

  it('active = status active, schedulable, no live rate-limit/cooldown', () => {
    expect(matchesStatusFilter(acct({}), 'active')).toBe(true)
    expect(matchesStatusFilter(acct({ schedulable: false }), 'active')).toBe(false)
    expect(matchesStatusFilter(acct({ rate_limit_reset_at: future }), 'active')).toBe(false)
    expect(matchesStatusFilter(acct({ temp_unschedulable_until: future }), 'active')).toBe(false)
    // An expired cooldown does not exclude an otherwise-active account.
    expect(matchesStatusFilter(acct({ temp_unschedulable_until: past }), 'active')).toBe(true)
  })

  it('rate_limited = active with a live rate-limit window and no live cooldown', () => {
    expect(matchesStatusFilter(acct({ rate_limit_reset_at: future }), 'rate_limited')).toBe(true)
    expect(matchesStatusFilter(acct({}), 'rate_limited')).toBe(false)
    expect(
      matchesStatusFilter(acct({ rate_limit_reset_at: future, temp_unschedulable_until: future }), 'rate_limited')
    ).toBe(false)
  })

  it('temp_unschedulable = active with a live cooldown', () => {
    expect(matchesStatusFilter(acct({ temp_unschedulable_until: future }), 'temp_unschedulable')).toBe(true)
    expect(matchesStatusFilter(acct({ temp_unschedulable_until: past }), 'temp_unschedulable')).toBe(false)
  })

  it('unschedulable = active but operator-paused, no live cooldown/rate-limit', () => {
    expect(matchesStatusFilter(acct({ schedulable: false }), 'unschedulable')).toBe(true)
    expect(matchesStatusFilter(acct({}), 'unschedulable')).toBe(false)
    expect(
      matchesStatusFilter(acct({ schedulable: false, rate_limit_reset_at: future }), 'unschedulable')
    ).toBe(false)
  })

  it('inactive / error match the literal status column', () => {
    expect(matchesStatusFilter(acct({ status: 'inactive' }), 'inactive')).toBe(true)
    expect(matchesStatusFilter(acct({ status: 'error' }), 'error')).toBe(true)
    expect(matchesStatusFilter(acct({ status: 'active' }), 'error')).toBe(false)
  })
})

describe('matchesGroupFilter', () => {
  it('matches everything when the filter is empty (all)', () => {
    expect(matchesGroupFilter(acct({ groups: ['default'] }), '')).toBe(true)
  })

  it('ungrouped matches only accounts with no groups', () => {
    expect(matchesGroupFilter(acct({ groups: [] }), 'ungrouped')).toBe(true)
    expect(matchesGroupFilter(acct({ groups: undefined }), 'ungrouped')).toBe(true)
    expect(matchesGroupFilter(acct({ groups: ['default'] }), 'ungrouped')).toBe(false)
  })

  it('a concrete group matches by name membership', () => {
    expect(matchesGroupFilter(acct({ groups: ['GPT 专线', 'default'] }), 'GPT 专线')).toBe(true)
    expect(matchesGroupFilter(acct({ groups: ['default'] }), 'antigravity')).toBe(false)
  })
})

describe('collectGroupNames', () => {
  it('returns a sorted, de-duplicated set across reachable edges only', () => {
    const edges: EdgeAccountsResult[] = [
      {
        edge_id: 'us3',
        base_url: 'https://api-us3.tokenkey.dev',
        ok: true,
        stub_schedulable: true,
        accounts: [acct({ groups: ['default', 'GPT 专线'] }), acct({ id: 2, groups: ['antigravity'] })]
      },
      {
        edge_id: 'uk1',
        base_url: 'https://api-uk1.tokenkey.dev',
        ok: true,
        stub_schedulable: true,
        accounts: [acct({ id: 3, groups: ['default'] })]
      },
      // Unreachable edge contributes nothing (no accounts payload to trust).
      { edge_id: 'dead', base_url: 'https://api-dead.tokenkey.dev', ok: false, stub_schedulable: true, accounts: [] }
    ]
    // Concrete expected order (localeCompare): de-duped 'default', sorted, edge 'dead' excluded.
    expect(collectGroupNames(edges)).toEqual(['antigravity', 'default', 'GPT 专线'])
  })
})

describe('accountVm', () => {
  it('returns the same memoized view-model for a stable account reference', () => {
    const a = acct({})
    expect(accountVm(a)).toBe(accountVm(a))
  })
})

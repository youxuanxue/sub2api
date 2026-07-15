import { describe, it, expect } from 'vitest'
import {
  isTempUnschedActive,
  toUsageInfo,
  matchesStatusFilter,
  edgeStubStatusBearing,
  matchesCombinedStatusFilter,
  matchesStubOnlyStatusFilter,
  matchesStubGroupFilter,
  collectStubGroupNames,
  filterDisplayEdges,
  isStubRateLimited,
  isStubTempUnschedActive,
  shouldShowEdgeAccountError,
  accountVm,
  toAccountLike,
  parseEdgeOperatorExpiresAt,
  edgeSubscriptionToCredentials,
  stripClassPrefix
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

// One edge slice with a healthy, schedulable, ungrouped prod stub by default; the
// stub_* overrides drive the group + combined-status filter cases.
function edge(over: Partial<EdgeAccountsResult>): EdgeAccountsResult {
  return {
    edge_id: 'us1',
    base_url: 'https://api-us1.tokenkey.dev',
    ok: true,
    stub_schedulable: true,
    stub_groups: [],
    accounts: [],
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

describe('shouldShowEdgeAccountError', () => {
  it('hides a stale error_message after the account recovered to active', () => {
    expect(
      shouldShowEdgeAccountError(
        acct({
          status: 'active',
          error_message: 'Access forbidden (403): The bearer token included in the request is invalid'
        })
      )
    ).toBe(false)
  })

  it('shows the error_message while the account status is still error', () => {
    expect(shouldShowEdgeAccountError(acct({ status: 'error', error_message: 'Access forbidden (403)' }))).toBe(true)
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

  it('forwards window_stats from edge local-window adapters', () => {
    const info = toUsageInfo(
      acct({
        platform: 'newapi',
        type: 'service_account',
        usage: {
          source: 'passive',
          five_hour: {
            utilization: 0,
            window_stats: {
              requests: 2,
              tokens: 2048,
              cost: 0.18,
              standard_cost: 0.18,
              user_cost: 0.18
            }
          },
          seven_day: {
            utilization: 0,
            window_stats: {
              requests: 7,
              tokens: 8192,
              cost: 0.72,
              standard_cost: 0.72,
              user_cost: 0.72
            }
          }
        }
      })
    )

    expect(info?.five_hour?.window_stats?.tokens).toBe(2048)
    expect(info?.seven_day?.window_stats?.requests).toBe(7)
  })

  it('lifts the edge kiro credits DTO into nested kiro_usage so the KiroUsageCell renders', () => {
    const info = toUsageInfo(
      acct({
        platform: 'kiro',
        type: 'oauth',
        usage: {
          source: 'passive',
          updated_at: '2026-07-10T00:00:00Z',
          upstream_quota: {
            provider: 'kiro',
            state: 'observed',
            credits: [
              {
                key: 'kiro_credits',
                current: 300,
                limit: 1000,
                remaining: 700
              }
            ]
          },
          kiro: {
            current: 300,
            limit: 1000,
            percent: 30,
            next_reset_date: '2026-07-01',
            subscription_title: 'Kiro Pro',
            trial_current: 5,
            trial_limit: 50,
            trial_percent: 10,
            trial_status: 'ACTIVE',
            trial_expires_at: '2026-07-15T00:00:00Z',
            bonuses: [
              {
                code: 'WELCOME500',
                label: 'Welcome Bonus',
                current: 120,
                limit: 500,
                percent: 24,
                status: 'ACTIVE',
                expires_at: '2026-08-01T00:00:00Z'
              }
            ]
          }
        }
      })
    )
    expect(info?.kiro_usage?.percent).toBe(30)
    expect(info?.kiro_usage?.limit).toBe(1000)
    expect(info?.kiro_usage?.next_reset_date).toBe('2026-07-01')
    expect(info?.kiro_usage?.subscription_title).toBe('Kiro Pro')
    expect(info?.kiro_usage?.trial?.percent).toBe(10)
    expect(info?.kiro_usage?.trial?.current).toBe(5)
    expect(info?.kiro_usage?.trial?.limit).toBe(50)
    expect(info?.kiro_usage?.trial?.expires_at).toBe('2026-07-15T00:00:00Z')
    expect(info?.kiro_usage?.bonuses?.[0]?.code).toBe('WELCOME500')
    expect(info?.upstream_quota?.provider).toBe('kiro')
    expect(info?.upstream_quota?.credits?.[0]?.remaining).toBe(700)
    expect(info?.updated_at).toBe('2026-07-10T00:00:00Z')
  })

  it('leaves kiro_usage null when the edge reported no kiro block', () => {
    const info = toUsageInfo(
      acct({ usage: { source: 'passive', five_hour: { utilization: 10 } } })
    )
    expect(info?.kiro_usage ?? null).toBeNull()
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

describe('matchesStubGroupFilter (分组 keyed on the PROD stub, edge-level)', () => {
  it('matches every edge when the filter is empty (all)', () => {
    expect(matchesStubGroupFilter(edge({ stub_groups: ['default'] }), '')).toBe(true)
    expect(matchesStubGroupFilter(edge({ stub_groups: [] }), '')).toBe(true)
  })

  it('ungrouped matches only edges whose stub belongs to no group', () => {
    expect(matchesStubGroupFilter(edge({ stub_groups: [] }), 'ungrouped')).toBe(true)
    expect(matchesStubGroupFilter(edge({ stub_groups: undefined }), 'ungrouped')).toBe(true)
    expect(matchesStubGroupFilter(edge({ stub_groups: ['default'] }), 'ungrouped')).toBe(false)
  })

  it('a concrete group matches by the stub-group-name membership', () => {
    expect(matchesStubGroupFilter(edge({ stub_groups: ['GPT 专线', 'default'] }), 'GPT 专线')).toBe(true)
    expect(matchesStubGroupFilter(edge({ stub_groups: ['default'] }), 'antigravity')).toBe(false)
  })

  it('ignores the edge-local account groups entirely (only the stub counts)', () => {
    // Account is in 'antigravity' but the stub is ungrouped → 'antigravity' filter
    // must NOT match: the page filters by the prod stub, not the edge accounts.
    const e = edge({ stub_groups: [], accounts: [acct({ groups: ['antigravity'] })] })
    expect(matchesStubGroupFilter(e, 'antigravity')).toBe(false)
    expect(matchesStubGroupFilter(e, 'ungrouped')).toBe(true)
  })
})

describe('collectStubGroupNames', () => {
  it('returns a sorted, de-duplicated set of stub groups across ALL edges (incl. unreachable)', () => {
    const edges: EdgeAccountsResult[] = [
      edge({ edge_id: 'us3', stub_groups: ['default', 'GPT 专线'] }),
      edge({ edge_id: 'uk1', stub_groups: ['default'] }),
      // Unreachable edge STILL contributes its stub group (known from the prod DB).
      edge({ edge_id: 'dead', ok: false, stub_groups: ['antigravity'] })
    ]
    // localeCompare order: de-duped 'default', 'dead' edge's 'antigravity' included.
    expect(collectStubGroupNames(edges)).toEqual(['antigravity', 'default', 'GPT 专线'])
  })
})

describe('edgeStubStatusBearing', () => {
  it('hard-codes status to active (stub is active-only) and maps the variable fields', () => {
    const future = new Date(Date.now() + 60 * 1000).toISOString()
    expect(edgeStubStatusBearing(edge({ stub_schedulable: false }))).toEqual({
      status: 'active',
      schedulable: false,
      rate_limit_reset_at: null,
      temp_unschedulable_until: null
    })
    expect(
      edgeStubStatusBearing(edge({ stub_rate_limit_reset_at: future, stub_temp_unschedulable_until: future }))
    ).toEqual({ status: 'active', schedulable: true, rate_limit_reset_at: future, temp_unschedulable_until: future })
  })
})

describe('isStubRateLimited / isStubTempUnschedActive (edge-header badges)', () => {
  const future = new Date(Date.now() + 60 * 1000).toISOString()
  const past = new Date(Date.now() - 60 * 1000).toISOString()

  it('true only while the stub cooldown is still in the future', () => {
    expect(isStubRateLimited(edge({ stub_rate_limit_reset_at: future }))).toBe(true)
    expect(isStubRateLimited(edge({ stub_rate_limit_reset_at: past }))).toBe(false)
    expect(isStubRateLimited(edge({ stub_rate_limit_reset_at: undefined }))).toBe(false)
    expect(isStubTempUnschedActive(edge({ stub_temp_unschedulable_until: future }))).toBe(true)
    expect(isStubTempUnschedActive(edge({ stub_temp_unschedulable_until: past }))).toBe(false)
    expect(isStubTempUnschedActive(edge({ stub_temp_unschedulable_until: undefined }))).toBe(false)
  })
})

describe('matchesCombinedStatusFilter (正常 = AND, others = OR)', () => {
  const future = new Date(Date.now() + 60 * 1000).toISOString()

  it('matches every row when the filter is empty (all)', () => {
    expect(matchesCombinedStatusFilter(acct({ status: 'error' }), edge({ stub_schedulable: false }), '')).toBe(true)
  })

  it('active requires BOTH the prod stub AND the edge account to be healthy', () => {
    // both healthy → match
    expect(matchesCombinedStatusFilter(acct({}), edge({}), 'active')).toBe(true)
    // stub 关调度 (schedulable=false) → NOT 正常 even though the account is healthy
    expect(matchesCombinedStatusFilter(acct({}), edge({ stub_schedulable: false }), 'active')).toBe(false)
    // stub rate-limited → NOT 正常 even though the account is healthy
    expect(matchesCombinedStatusFilter(acct({}), edge({ stub_rate_limit_reset_at: future }), 'active')).toBe(false)
    // account paused → NOT 正常 even though the stub is healthy
    expect(matchesCombinedStatusFilter(acct({ schedulable: false }), edge({}), 'active')).toBe(false)
  })

  it('an abnormal bucket matches if EITHER the stub OR the account is in that state', () => {
    // stub rate-limited, account healthy → rate_limited matches (via the stub)
    expect(matchesCombinedStatusFilter(acct({}), edge({ stub_rate_limit_reset_at: future }), 'rate_limited')).toBe(true)
    // account rate-limited, stub healthy → rate_limited matches (via the account)
    expect(matchesCombinedStatusFilter(acct({ rate_limit_reset_at: future }), edge({}), 'rate_limited')).toBe(true)
    // neither rate-limited → no match
    expect(matchesCombinedStatusFilter(acct({}), edge({}), 'rate_limited')).toBe(false)
    // stub 关调度 → 'unschedulable' matches via the stub (the 调度已关闭 edge case)
    expect(matchesCombinedStatusFilter(acct({}), edge({ stub_schedulable: false }), 'unschedulable')).toBe(true)
    // account inactive → 'inactive' matches via the account (stub is always active)
    expect(matchesCombinedStatusFilter(acct({ status: 'inactive' }), edge({}), 'inactive')).toBe(true)
  })
})

describe('matchesStubOnlyStatusFilter (unreachable edges, stub alone)', () => {
  const future = new Date(Date.now() + 60 * 1000).toISOString()

  it('matches when the filter is empty (all)', () => {
    expect(matchesStubOnlyStatusFilter(edge({ ok: false, stub_schedulable: false }), '')).toBe(true)
  })

  it('never shows an unreachable edge under the 正常 filter', () => {
    expect(matchesStubOnlyStatusFilter(edge({ ok: false }), 'active')).toBe(false)
  })

  it('shows an unreachable edge under an abnormal filter iff the stub is in that state', () => {
    expect(matchesStubOnlyStatusFilter(edge({ ok: false, stub_rate_limit_reset_at: future }), 'rate_limited')).toBe(true)
    expect(matchesStubOnlyStatusFilter(edge({ ok: false, stub_schedulable: false }), 'unschedulable')).toBe(true)
    expect(matchesStubOnlyStatusFilter(edge({ ok: false }), 'rate_limited')).toBe(false)
  })
})

describe('filterDisplayEdges (orchestration)', () => {
  const future = new Date(Date.now() + 60 * 1000).toISOString()

  it('returns the SAME array reference when no filter is active (merge stability)', () => {
    const edges = [edge({ accounts: [acct({})] })]
    expect(filterDisplayEdges(edges, '', '')).toBe(edges)
  })

  it('drops a reachable edge whose stub group does not match', () => {
    const edges = [
      edge({ edge_id: 'us1', stub_groups: ['default'], accounts: [acct({})] }),
      edge({ edge_id: 'us2', stub_groups: ['antigravity'], accounts: [acct({ id: 2 })] })
    ]
    const out = filterDisplayEdges(edges, '', 'default')
    expect(out.map((e) => e.edge_id)).toEqual(['us1'])
  })

  it('keeps all accounts of an edge whose stub is rate-limited under the rate_limited filter', () => {
    // Stub rate-limited, both accounts healthy → OR via stub keeps both rows.
    const edges = [edge({ stub_rate_limit_reset_at: future, accounts: [acct({ id: 1 }), acct({ id: 2 })] })]
    const out = filterDisplayEdges(edges, 'rate_limited', '')
    expect(out).toHaveLength(1)
    expect(out[0].accounts.map((a) => a.id)).toEqual([1, 2])
  })

  it('only keeps 正常 rows when both the stub and the account are healthy', () => {
    const edges = [
      edge({ edge_id: 'ok', accounts: [acct({ id: 1 }), acct({ id: 2, schedulable: false })] }),
      // Same accounts but the stub is 关调度 → none of its rows are 正常.
      edge({ edge_id: 'paused', stub_schedulable: false, accounts: [acct({ id: 3 })] })
    ]
    const out = filterDisplayEdges(edges, 'active', '')
    expect(out.map((e) => e.edge_id)).toEqual(['ok'])
    expect(out[0].accounts.map((a) => a.id)).toEqual([1]) // id:2 (paused account) dropped
  })

  it('matches an unreachable edge on the stub alone, gated by the group filter', () => {
    const edges = [
      edge({ edge_id: 'dead-rl', ok: false, stub_groups: ['default'], stub_rate_limit_reset_at: future }),
      edge({ edge_id: 'dead-other', ok: false, stub_groups: ['antigravity'], stub_rate_limit_reset_at: future })
    ]
    // rate_limited + group=default → only the matching-group unreachable edge shows.
    expect(filterDisplayEdges(edges, 'rate_limited', 'default').map((e) => e.edge_id)).toEqual(['dead-rl'])
    // active filter hides every unreachable edge regardless of group.
    expect(filterDisplayEdges(edges, 'active', '')).toHaveLength(0)
  })

  it('surfaces a REACHABLE-but-empty edge on the stub alone (symmetry with unreachable)', () => {
    // Reachable edge, zero local accounts, stub rate-limited → must still show under
    // the rate_limited filter (kept with an empty accounts array), exactly like its
    // unreachable counterpart — honoring the "stub OR account" rule symmetrically.
    const edges = [edge({ edge_id: 'fresh', ok: true, accounts: [], stub_rate_limit_reset_at: future })]
    const rl = filterDisplayEdges(edges, 'rate_limited', '')
    expect(rl.map((e) => e.edge_id)).toEqual(['fresh'])
    expect(rl[0].accounts).toEqual([])
    // But not under the 正常 filter (no healthy rows to confirm), nor when the stub is healthy.
    expect(filterDisplayEdges(edges, 'active', '')).toHaveLength(0)
    expect(filterDisplayEdges([edge({ ok: true, accounts: [] })], 'rate_limited', '')).toHaveLength(0)
  })
})

describe('accountVm', () => {
  it('returns the same memoized view-model for a stable account reference', () => {
    const a = acct({})
    expect(accountVm(a)).toBe(accountVm(a))
  })
})

describe('stripClassPrefix', () => {
  it('strips the anthropic:class: prefix so the shared badge can alias the bare class', () => {
    const reset = '2026-06-21T10:00:00Z'
    const out = stripClassPrefix({
      'anthropic:class:sonnet': {
        rate_limited_at: '2026-06-21T09:00:00Z',
        rate_limit_reset_at: reset,
        reason: 'anthropic_unified_window_exceeded'
      }
    })
    expect(Object.keys(out)).toEqual(['sonnet'])
    expect(out.sonnet.rate_limit_reset_at).toBe(reset)
    expect(out.sonnet.rate_limited_at).toBe('2026-06-21T09:00:00Z')
  })

  it('falls back rate_limited_at to reset when omitted, and passes non-prefixed keys through', () => {
    const reset = '2026-06-21T10:00:00Z'
    const out = stripClassPrefix({
      'anthropic:class:opus': { rate_limit_reset_at: reset },
      AICredits: { rate_limit_reset_at: reset }
    })
    expect(out.opus.rate_limited_at).toBe(reset)
    expect(out.AICredits.rate_limit_reset_at).toBe(reset)
  })
})

describe('toAccountLike subscription + operator expiry', () => {
  it('maps edge subscription snapshot into credentials for PlatformTypeBadge parity', () => {
    const result = toAccountLike(
      acct({
        subscription: { plan_type: 'plus', expires_at: '2026-12-01T00:00:00Z' }
      })
    )
    expect(result.credentials).toEqual({
      plan_type: 'plus',
      subscription_expires_at: '2026-12-01T00:00:00Z'
    })
  })

  it('parses operator expires_at ISO into unix seconds', () => {
    const iso = '2026-06-01T12:00:00.000Z'
    expect(parseEdgeOperatorExpiresAt(iso)).toBe(Math.floor(Date.parse(iso) / 1000))
    expect(toAccountLike(acct({ expires_at: iso })).expires_at).toBe(
      parseEdgeOperatorExpiresAt(iso)
    )
  })

  it('leaves credentials undefined when subscription is absent', () => {
    expect(edgeSubscriptionToCredentials(undefined)).toBeUndefined()
    expect(toAccountLike(acct({ subscription: undefined })).credentials).toBeUndefined()
  })
})

describe('toAccountLike model_rate_limits → extra', () => {
  it('lights up extra.model_rate_limits with the prefix stripped', () => {
    const reset = new Date(Date.now() + 60 * 60 * 1000).toISOString()
    const result = toAccountLike(
      acct({
        model_rate_limits: {
          'anthropic:class:sonnet': {
            rate_limited_at: '2026-06-21T09:00:00Z',
            rate_limit_reset_at: reset,
            reason: 'anthropic_unified_window_exceeded'
          }
        }
      })
    )
    expect(result.extra?.model_rate_limits).toEqual({
      sonnet: { rate_limited_at: '2026-06-21T09:00:00Z', rate_limit_reset_at: reset }
    })
  })

  it('leaves extra undefined (not {}) when no model_rate_limits present', () => {
    expect(toAccountLike(acct({ model_rate_limits: undefined })).extra).toBeUndefined()
  })
})

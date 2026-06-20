import { describe, it, expect } from 'vitest'
import {
  edgeAccountIsAbnormal,
  edgePanelHasAnomaly,
  edgePanelCounts,
  edgePanelAbnormalCount,
  sortEdgeAccountsAbnormalFirst,
  isStubPanelExpanded
} from '@/utils/accountsEdgePanels.tk'
import type { EdgeAccountSummary, EdgeAccountsResult } from '@/api/admin/edgeAccounts'

function acct(over: Partial<EdgeAccountSummary>): EdgeAccountSummary {
  return {
    id: 1,
    name: 'edge-acct',
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

function edge(over: Partial<EdgeAccountsResult>): EdgeAccountsResult {
  return {
    edge_id: 'us4',
    base_url: 'https://api-us4.tokenkey.dev',
    ok: true,
    stub_schedulable: true,
    stub_groups: [],
    accounts: [],
    ...over
  }
}

const future = () => new Date(Date.now() + 60_000).toISOString()
const past = () => new Date(Date.now() - 60_000).toISOString()

describe('edgeAccountIsAbnormal', () => {
  it('is false for a healthy active account', () => {
    expect(edgeAccountIsAbnormal(acct({}))).toBe(false)
  })
  it('is true for error / inactive status', () => {
    expect(edgeAccountIsAbnormal(acct({ status: 'error' }))).toBe(true)
    expect(edgeAccountIsAbnormal(acct({ status: 'inactive' }))).toBe(true)
  })
  it('is true while a rate-limit / temp-unsched / overload cooldown is LIVE', () => {
    expect(edgeAccountIsAbnormal(acct({ rate_limit_reset_at: future() }))).toBe(true)
    expect(edgeAccountIsAbnormal(acct({ temp_unschedulable_until: future() }))).toBe(true)
    expect(edgeAccountIsAbnormal(acct({ overload_until: future() }))).toBe(true)
  })
  it('is false for a LAPSED cooldown (stale breadcrumb), not a live problem', () => {
    expect(
      edgeAccountIsAbnormal(acct({ temp_unschedulable_until: past(), temp_unschedulable_reason: '429' }))
    ).toBe(false)
  })
  it('does NOT treat an operator-paused (schedulable=false) account as abnormal', () => {
    // Intentional pause is not a problem to surface; only genuine faults expand.
    expect(edgeAccountIsAbnormal(acct({ schedulable: false, is_schedulable: false }))).toBe(false)
  })
})

describe('edgePanelHasAnomaly', () => {
  it('is false for a fully healthy edge', () => {
    expect(edgePanelHasAnomaly(edge({ accounts: [acct({})] }))).toBe(false)
  })
  it('is true for an unreachable edge', () => {
    expect(edgePanelHasAnomaly(edge({ ok: false, error: 'timeout' }))).toBe(true)
  })
  it('is true when the prod stub is paused (关调度)', () => {
    expect(edgePanelHasAnomaly(edge({ stub_schedulable: false, accounts: [acct({})] }))).toBe(true)
  })
  it('is true when the stub is rate-limited / temp-unschedulable', () => {
    expect(edgePanelHasAnomaly(edge({ stub_rate_limit_reset_at: future(), accounts: [acct({})] }))).toBe(true)
    expect(edgePanelHasAnomaly(edge({ stub_temp_unschedulable_until: future(), accounts: [acct({})] }))).toBe(true)
  })
  it('is true when ANY edge account is abnormal', () => {
    expect(edgePanelHasAnomaly(edge({ accounts: [acct({}), acct({ id: 2, status: 'error' })] }))).toBe(true)
  })
})

describe('edgePanelCounts', () => {
  it('counts total and effectively-schedulable accounts', () => {
    const e = edge({
      accounts: [acct({ is_schedulable: true }), acct({ id: 2, is_schedulable: false }), acct({ id: 3, is_schedulable: true })]
    })
    expect(edgePanelCounts(e)).toEqual({ total: 3, schedulable: 2 })
  })
})

describe('isStubPanelExpanded (v2: default-full-expand)', () => {
  it('an explicit override ALWAYS wins', () => {
    expect(isStubPanelExpanded(false)).toBe(false) // user collapsed → stays collapsed
    expect(isStubPanelExpanded(true)).toBe(true) // user expanded → stays expanded
  })
  it('DEFAULT (no override) is expanded (一目了然)', () => {
    expect(isStubPanelExpanded(undefined)).toBe(true)
  })
})

describe('edgePanelAbnormalCount', () => {
  it('counts only attention-worthy accounts', () => {
    const e = edge({
      accounts: [acct({}), acct({ id: 2, status: 'error' }), acct({ id: 3, rate_limit_reset_at: future() })]
    })
    expect(edgePanelAbnormalCount(e)).toBe(2)
  })
})

describe('sortEdgeAccountsAbnormalFirst', () => {
  it('floats abnormal accounts to the top, stable within bands by priority then id', () => {
    const input = [
      acct({ id: 1, priority: 5 }),
      acct({ id: 2, status: 'error', priority: 9 }),
      acct({ id: 3, priority: 1 }),
      acct({ id: 4, rate_limit_reset_at: future(), priority: 2 })
    ]
    const out = sortEdgeAccountsAbnormalFirst(input)
    // abnormal (2, 4) first ordered by priority (4@2 before 2@9); then healthy (3@1, 1@5)
    expect(out.map((a) => a.id)).toEqual([4, 2, 3, 1])
    // input not mutated
    expect(input.map((a) => a.id)).toEqual([1, 2, 3, 4])
  })
})

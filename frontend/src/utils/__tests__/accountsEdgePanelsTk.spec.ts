import { describe, it, expect } from 'vitest'
import {
  edgeAccountIsAbnormal,
  edgePanelHasAnomaly,
  compositeEdgeAccountKey,
  edgePanelCounts,
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

describe('compositeEdgeAccountKey', () => {
  it('namespaces an edge-local id so it never collides with a prod stub id', () => {
    expect(compositeEdgeAccountKey('us4', 51)).toBe('edge:us4:51')
    // Same numeric id on different edges → distinct keys.
    expect(compositeEdgeAccountKey('uk2', 51)).not.toBe(compositeEdgeAccountKey('us4', 51))
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

describe('isStubPanelExpanded (expand priority)', () => {
  const healthy = edge({ accounts: [acct({})] })
  const anomalous = edge({ ok: false })

  it('an explicit override ALWAYS wins (even over search + anomaly)', () => {
    // user collapsed → stays collapsed despite searching and an anomaly
    expect(isStubPanelExpanded(false, true, anomalous)).toBe(false)
    // user expanded → stays expanded despite a healthy edge and no search
    expect(isStubPanelExpanded(true, false, healthy)).toBe(true)
  })
  it('searching auto-expands when there is no override', () => {
    expect(isStubPanelExpanded(undefined, true, healthy)).toBe(true)
  })
  it('without override or search, follows the edge anomaly state', () => {
    expect(isStubPanelExpanded(undefined, false, anomalous)).toBe(true)
    expect(isStubPanelExpanded(undefined, false, healthy)).toBe(false)
  })
  it('an undiscovered edge (null) stays collapsed unless overridden / searching', () => {
    expect(isStubPanelExpanded(undefined, false, null)).toBe(false)
    expect(isStubPanelExpanded(undefined, true, null)).toBe(true)
    expect(isStubPanelExpanded(true, false, null)).toBe(true)
  })
})

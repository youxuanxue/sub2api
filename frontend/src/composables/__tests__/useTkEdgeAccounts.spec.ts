import { describe, it, expect } from 'vitest'
import { mergeEdges } from '@/composables/useTkEdgeAccounts'
import type { EdgeAccountsResult } from '@/api/admin/edgeAccounts'

function edge(id: string, accounts: EdgeAccountsResult['accounts'] = []): EdgeAccountsResult {
  return { edge_id: id, base_url: `https://api-${id}.tokenkey.dev`, ok: true, stub_schedulable: true, accounts }
}

describe('mergeEdges', () => {
  it('keeps the same array reference when nothing changed', () => {
    const cur = [edge('us1'), edge('fra1')]
    const next = [edge('us1'), edge('fra1')]
    // Same content → must return the ORIGINAL array (no re-render churn).
    expect(mergeEdges(cur, next)).toBe(cur)
  })

  it('preserves the reference of unchanged edges, replaces only changed ones', () => {
    const us1 = edge('us1')
    const fra1 = edge('fra1')
    const cur = [us1, fra1]
    const next = [edge('us1'), edge('fra1', [{ id: 1 } as never])] // fra1 changed
    const merged = mergeEdges(cur, next)
    expect(merged).not.toBe(cur)
    expect(merged[0]).toBe(us1) // unchanged → same reference
    expect(merged[1]).not.toBe(fra1) // changed → new object
    expect(merged[1].accounts).toHaveLength(1)
  })

  it('rebuilds when an edge is added or removed', () => {
    const cur = [edge('us1')]
    expect(mergeEdges(cur, [edge('us1'), edge('fra1')])).toHaveLength(2)
    expect(mergeEdges([edge('us1'), edge('fra1')], [edge('us1')])).toHaveLength(1)
  })

  it('detects pure reordering (same set, different order)', () => {
    const cur = [edge('us1'), edge('fra1')]
    const merged = mergeEdges(cur, [edge('fra1'), edge('us1')])
    expect(merged).not.toBe(cur)
    expect(merged.map((e) => e.edge_id)).toEqual(['fra1', 'us1'])
  })
})

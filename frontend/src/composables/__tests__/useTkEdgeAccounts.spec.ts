import { mount } from '@vue/test-utils'
import { defineComponent, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mergeEdges, useTkEdgeAccounts } from '@/composables/useTkEdgeAccounts'
import type { EdgeAccountsResult } from '@/api/admin/edgeAccounts'

const mocks = vi.hoisted(() => ({
  listWithEtag: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    edgeAccounts: {
      listWithEtag: mocks.listWithEtag
    }
  }
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

function edge(id: string, accounts: EdgeAccountsResult['accounts'] = []): EdgeAccountsResult {
  return { edge_id: id, base_url: `https://api-${id}.tokenkey.dev`, ok: true, stub_schedulable: true, accounts }
}

async function flushPromises() {
  await Promise.resolve()
  await nextTick()
}

beforeEach(() => {
  localStorage.clear()
  mocks.listWithEtag.mockReset()
  mocks.listWithEtag.mockResolvedValue({
    notModified: false,
    etag: '"edge-etag"',
    data: { platform: '__by_stub__', edges: [], ts: 1 }
  })
  Object.defineProperty(document, 'hidden', { value: false, configurable: true })
})

afterEach(() => {
  vi.useRealTimers()
  localStorage.clear()
})

describe('useTkEdgeAccounts refresh cadence', () => {
  it.each([15, 60, 5, 10])('honors the migrated cadence for saved interval %s', async (savedInterval) => {
    vi.useFakeTimers()
    localStorage.setItem('edge-accounts-auto-refresh', JSON.stringify({ enabled: true, interval: savedInterval }))
    const wrapper = mount(defineComponent({
      setup() {
        useTkEdgeAccounts()
        return () => null
      }
    }))
    try {
      await flushPromises()
      mocks.listWithEtag.mockClear()
      const interval = savedInterval < 15 ? 30 : savedInterval
      await vi.advanceTimersByTimeAsync(interval * 1000)
      expect(mocks.listWithEtag).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1000)
      expect(mocks.listWithEtag).toHaveBeenCalledTimes(1)
    } finally {
      wrapper.unmount()
    }
  })
})

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

describe('useTkEdgeAccounts by-stub freshness', () => {
  it('force-refreshes the inline prod /accounts panel when the tab becomes visible again', async () => {
    const wrapper = mount(defineComponent({
      setup() {
        useTkEdgeAccounts('all', { byStub: true, enabled: () => true })
        return () => null
      }
    }))
    await flushPromises()

    mocks.listWithEtag.mockClear()
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()

    expect(mocks.listWithEtag).toHaveBeenCalledWith(
      { view: 'by-stub' },
      { force: true }
    )
    wrapper.unmount()
  })

  it('does not install the visibility refresh for the standalone edge overview', async () => {
    const wrapper = mount(defineComponent({
      setup() {
        useTkEdgeAccounts('all', { enabled: () => true })
        return () => null
      }
    }))
    await flushPromises()

    mocks.listWithEtag.mockClear()
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()

    expect(mocks.listWithEtag).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
